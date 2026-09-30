package reversesshfs

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"unsafe"

	"github.com/pkg/sftp"
	"golang.org/x/sys/windows"
)

func ctime(t *testing.T, p string) int64 {
	t.Helper()
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var info fileBasicInformation
	if err := windows.GetFileInformationByHandleEx(windows.Handle(f.Fd()), windows.FileBasicInfo,
		(*byte)(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		t.Fatal(err)
	}
	return info.ChangeTime
}

func TestSlashPath(t *testing.T) {
	for in, want := range map[string]string{
		`C:\Users\foo`:       "/C:/Users/foo",
		`C:/Users/foo`:       "/C:/Users/foo",
		`/C:/Users/foo/`:     "/C:/Users/foo",
		`C:\`:                "/C:",
		`\\server\share\foo`: "/server/share/foo",
	} {
		if got := slashPath(in); got != want {
			t.Errorf("slashPath(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{
		"C:/Users/foo/bar":  "/C:/Users/foo/bar",
		"/C:/Users/foo/bar": "/C:/Users/foo/bar",
		"bar":               "/C:/Users/foo/bar",
		".":                 "/C:/Users/foo",
	} {
		if got := realPath("/C:/Users/foo", in); got != want {
			t.Errorf("realPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func shortBase(t *testing.T, p string) string {
	t.Helper()
	p16, err := windows.UTF16PtrFromString(p)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]uint16, windows.MAX_PATH)
	n, err := windows.GetShortPathName(p16, &buf[0], uint32(len(buf)))
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Base(windows.UTF16ToString(buf[:n]))
}

// TestRootedDeniesShortNames writes through the 8.3 short name of .git (CVE-2019-1353),
// which passes the lexical check.
func TestRootedDeniesShortNames(t *testing.T) {
	type env struct {
		c         *sftp.Client
		root, git string // git is the short name of .git
		gitfile   string // the short name of src/.git
	}
	p := func(e env, s string) string { return filepath.Join(e.root, s) }
	open := func(e env, s string, flags int) error {
		f, err := e.c.OpenFile(p(e, s), flags)
		if err == nil {
			f.Close()
		}
		return err
	}
	attempts := []struct {
		name string
		f    func(e env) error
	}{
		{"create hook", func(e env) error { return open(e, e.git+"/hooks/pre-commit", os.O_RDWR|os.O_CREATE|os.O_TRUNC) }},
		{"open config for write", func(e env) error { return open(e, e.git+"/config", os.O_WRONLY) }},
		{"truncate on open", func(e env) error { return open(e, e.git+"/config", os.O_WRONLY|os.O_TRUNC) }},
		{"remove config", func(e env) error { return e.c.Remove(p(e, e.git+"/config")) }},
		{"rmdir hooks", func(e env) error { return e.c.RemoveDirectory(p(e, e.git+"/hooks")) }},
		{"rename .git away", func(e env) error { return e.c.PosixRename(p(e, e.git), p(e, "old")) }},
		{"rename into .git", func(e env) error { return e.c.PosixRename(p(e, "src/main.go"), p(e, e.git+"/hooks/x")) }},
		{"hardlink into hooks", func(e env) error { return e.c.Link(p(e, "src/main.go"), p(e, e.git+"/hooks/x")) }},
		{"chmod config", func(e env) error { return e.c.Chmod(p(e, e.git+"/config"), 0o444) }},
		{"truncate config", func(e env) error { return e.c.Truncate(p(e, e.git+"/config"), 0) }},
		{"write gitfile", func(e env) error { return open(e, "src/"+e.gitfile, os.O_WRONLY|os.O_TRUNC) }},
		{"replace gitfile", func(e env) error { return e.c.PosixRename(p(e, "src/main.go"), p(e, "src/"+e.gitfile)) }},
		{"remove gitfile", func(e env) error { return e.c.Remove(p(e, "src/"+e.gitfile)) }},
	}
	for _, a := range attempts {
		t.Run(a.name, func(t *testing.T) {
			c, root := setupRooted(t, false)
			gitfileLong := filepath.Join(root, "src", ".git")
			if err := os.WriteFile(gitfileLong, []byte("gitdir: x"), 0o644); err != nil {
				t.Fatal(err)
			}
			e := env{c: c, root: root, git: shortBase(t, filepath.Join(root, ".git")), gitfile: shortBase(t, gitfileLong)}
			if strings.EqualFold(e.git, ".git") || strings.EqualFold(e.gitfile, ".git") {
				t.Skip("8.3 names are not created on this volume")
			}
			if err := a.f(e); err == nil {
				t.Error("expected an error")
			}
			assertUnchanged(t, root)
			if b, err := os.ReadFile(gitfileLong); err != nil || string(b) != "gitdir: x" {
				t.Fatalf("src/.git changed: %q, %v", b, err)
			}
		})
	}
}

func TestRootedDeniesWindowsNames(t *testing.T) {
	junction := func(t *testing.T, root string) {
		t.Helper()
		// Unlike a symlink, a junction needs no privilege.
		out, err := exec.Command("cmd", "/c", "mklink", "/J", filepath.Join(root, "jn"), filepath.Join(root, ".git")).CombinedOutput()
		if err != nil {
			t.Fatalf("mklink: %v: %s", err, out)
		}
	}
	attempts := []struct {
		name  string
		setup func(t *testing.T, root string)
		path  string
	}{
		{"stream of .git", nil, ".git::$INDEX_ALLOCATION/hooks/pre-commit"},
		{"stream of a file", nil, "src/main.go:x"},
		{"backslashes", nil, `src\..\.git\hooks\pre-commit`},
		{"via junction", junction, "jn/hooks/pre-commit"},
		{"via junction to a file", junction, "jn/config"},
	}
	for _, a := range attempts {
		t.Run(a.name, func(t *testing.T) {
			c, root := setupRooted(t, false)
			if a.setup != nil {
				a.setup(t, root)
			}
			if f, err := c.OpenFile(root+`\`+a.path, os.O_RDWR|os.O_CREATE|os.O_TRUNC); err == nil {
				f.Close()
				t.Error("expected an error")
			}
			assertUnchanged(t, root)
			if fis, err := os.ReadDir(filepath.Join(root, "src")); err != nil || len(fis) != 1 {
				t.Fatalf("src entries changed: %v, %v", fis, err)
			}
		})
	}
}

func TestRootedSymlinkUnsupported(t *testing.T) {
	c, root := setupRooted(t, false)
	if err := c.Symlink("main.go", filepath.Join(root, "src", "link")); err == nil {
		t.Fatal("expected an error")
	}
	if _, err := os.Lstat(filepath.Join(root, "src", "link")); err == nil {
		t.Fatal("the symlink was created")
	}
}
