//go:build linux || darwin || windows

package reversesshfs

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/lima-vm/sshocker/pkg/util"
	"github.com/pkg/sftp"
)

var (
	zwnj = string(rune(0x200c)) // ignored by HFS+
	zwj  = string(rune(0x200d))
	bom  = string(rune(0xfeff))
)

// setupRooted serves <tmp>/root with ".git" read-only, and returns a client
// that plays the role of a compromised guest sending arbitrary requests.
// It also creates the symlinks "gitlink" -> ".git" and "configlink" -> ".git/config".
func setupRooted(t *testing.T, readonly bool) (*sftp.Client, string) {
	t.Helper()
	tmp := t.TempDir()
	root := filepath.Join(tmp, "root")
	for _, d := range []string{filepath.Join(root, ".git", "hooks"), filepath.Join(root, "src"), filepath.Join(tmp, "outside")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{filepath.Join(root, ".git", "config"), filepath.Join(root, "src", "main.go"), filepath.Join(tmp, "outside", "secret")} {
		if err := os.WriteFile(f, []byte("orig"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(".git", filepath.Join(root, "gitlink")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(".git", "config"), filepath.Join(root, "configlink")); err != nil {
		t.Fatal(err)
	}
	c2sR, c2sW := io.Pipe()
	s2cR, s2cW := io.Pipe()
	srv, h, err := newRootedServer(&util.RWC{ReadCloser: c2sR, WriteCloser: s2cW}, root, readonly, []string{".git"})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.Serve()
		_ = h.Close()
	}()
	client, err := sftp.NewClientPipe(s2cR, c2sW)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		srv.Close()
		client.Close()
		<-done
	})
	return client, root
}

func assertUnchanged(t *testing.T, root string) {
	t.Helper()
	fi, err := os.Lstat(filepath.Join(root, ".git"))
	if err != nil || !fi.IsDir() {
		t.Fatalf(".git is no longer a directory: %v, %v", fi, err)
	}
	b, err := os.ReadFile(filepath.Join(root, ".git", "config"))
	if err != nil || string(b) != "orig" {
		t.Fatalf(".git/config changed: %q, %v", b, err)
	}
	wantPerm := os.FileMode(0o644)
	if runtime.GOOS == "windows" {
		wantPerm = 0o666
	}
	fi, err = os.Stat(filepath.Join(root, ".git", "config"))
	if err != nil || fi.Mode().Perm() != wantPerm {
		t.Fatalf(".git/config mode changed: %v, %v", fi, err)
	}
	b, err = os.ReadFile(filepath.Join(filepath.Dir(root), "outside", "secret"))
	if err != nil || string(b) != "orig" {
		t.Fatalf("outside/secret changed: %q, %v", b, err)
	}
	for dir, n := range map[string]int{".git": 2, ".git/hooks": 0, "../outside": 1} {
		entries, err := os.ReadDir(filepath.Join(root, dir))
		if err != nil || len(entries) != n {
			t.Fatalf("%s entries changed: %v, %v", dir, entries, err)
		}
	}
}

func TestRootedAllowsNormalOperations(t *testing.T) {
	c, root := setupRooted(t, false)
	p := func(s string) string { return filepath.Join(root, s) }

	f, err := c.Create(p("src/new.go"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	if err := f.Chmod(0o600); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if err := c.Truncate(p("src/new.go"), 2); err != nil {
		t.Fatal(err)
	}
	mtime := time.Unix(1_000_000_000, 0)
	if err := c.Chtimes(p("src/new.go"), mtime, mtime); err != nil {
		t.Fatal(err)
	}
	if err := c.Mkdir(p("dir")); err != nil {
		t.Fatal(err)
	}
	if err := c.PosixRename(p("src/new.go"), p("dir/renamed.go")); err != nil {
		t.Fatal(err)
	}
	if err := c.Rename(p("dir/renamed.go"), p("src/main.go")); err == nil {
		t.Fatal("Rename must not replace an existing file")
	}
	names := []string{"dir/hardlink", "dir/renamed.go"}
	statName, wantPerm := "dir/renamed.go", os.FileMode(0o666) // Windows keeps only the owner write bit
	if runtime.GOOS != "windows" {
		if err := c.Symlink("renamed.go", p("dir/link")); err != nil {
			t.Fatal(err)
		}
		if target, err := c.ReadLink(p("dir/link")); err != nil || target != "renamed.go" {
			t.Fatalf("ReadLink: %q, %v", target, err)
		}
		names = append(names, "dir/link")
		statName, wantPerm = "dir/link", 0o600
	}
	fi, err := c.Stat(p(statName))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Size() != 2 || fi.Mode().Perm() != wantPerm || !fi.ModTime().Equal(mtime) {
		t.Fatalf("unexpected stat: size=%d mode=%v mtime=%v", fi.Size(), fi.Mode(), fi.ModTime())
	}
	if err := c.Link(p("dir/renamed.go"), p("dir/hardlink")); err != nil {
		t.Fatal(err)
	}
	entries, err := c.ReadDir(p("dir"))
	if err != nil || len(entries) != len(names) {
		t.Fatalf("ReadDir: %v, %v", entries, err)
	}
	if _, err := c.StatVFS(root); err != nil {
		t.Fatal(err)
	}
	for _, s := range names {
		if err := c.Remove(p(s)); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.RemoveDirectory(p("dir")); err != nil {
		t.Fatal(err)
	}
	// Reading the protected directory, also through an in-root symlink, is allowed.
	rf, err := c.Open(p("configlink"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(rf)
	rf.Close()
	if err != nil || string(b) != "orig" {
		t.Fatalf("read .git/config: %q, %v", b, err)
	}
	assertUnchanged(t, root)
}

func TestRootedChmodWithoutReadPermission(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("mode bits other than the owner write bit are not supported on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root bypasses permission checks")
	}
	c, root := setupRooted(t, false)
	for _, s := range []string{"src/main.go", "src"} {
		p := filepath.Join(root, s)
		if err := os.Chmod(p, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(p, 0o755) })
		if err := c.Chmod(p, 0o755); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
		if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != 0o755 {
			t.Fatalf("%s: %v, %v", s, fi, err)
		}
	}
}

func TestRootedDeniesProtectedWrites(t *testing.T) {
	type env struct {
		c    *sftp.Client
		root string
	}
	p := func(e env, s string) string { return filepath.Join(e.root, s) }
	open := func(e env, s string, flags int) error {
		f, err := e.c.OpenFile(p(e, s), flags)
		if err == nil {
			f.Close()
		}
		return err
	}
	create := func(e env, s string) error { return open(e, s, os.O_RDWR|os.O_CREATE|os.O_TRUNC) }
	withOutsideLink := func(f func(e env) error) func(e env) error {
		return func(e env) error {
			if err := os.Symlink(filepath.Join(e.root, "..", "outside"), p(e, "out")); err != nil {
				panic(err)
			}
			return f(e)
		}
	}
	attempts := []struct {
		name string
		f    func(e env) error
	}{
		{"create hook", func(e env) error { return create(e, ".git/hooks/pre-commit") }},
		{"open config for write", func(e env) error { return open(e, ".git/config", os.O_WRONLY) }},
		// On a case-insensitive file system, these would name the existing .git.
		{"upper case", func(e env) error { return e.c.Mkdir(p(e, "src/.GIT")) }},
		{"hfs ignorable", func(e env) error { return e.c.Mkdir(p(e, "src/.g"+zwnj+"it")) }},
		{"nested repo", func(e env) error { return e.c.Mkdir(p(e, "src/.git")) }},
		{"nested gitfile", func(e env) error { return create(e, "src/.git") }},
		{"via dir symlink", func(e env) error { return create(e, "gitlink/hooks/pre-commit") }},
		{"via file symlink", func(e env) error { return open(e, "configlink", os.O_WRONLY|os.O_TRUNC) }},
		{"hardlink from config", func(e env) error { return e.c.Link(p(e, ".git/config"), p(e, "src/config")) }},
		{"hardlink into hooks", func(e env) error { return e.c.Link(p(e, "src/main.go"), p(e, ".git/hooks/x")) }},
		{"symlink in hooks", func(e env) error { return e.c.Symlink("/bin/sh", p(e, ".git/hooks/x")) }},
		{"rename .git away", func(e env) error { return e.c.PosixRename(p(e, ".git"), p(e, "old")) }},
		{"rename into .git", func(e env) error { return e.c.PosixRename(p(e, "src/main.go"), p(e, ".git/hooks/x")) }},
		{"rename to nested gitfile", func(e env) error { return e.c.PosixRename(p(e, "src/main.go"), p(e, "src/.git")) }},
		{"remove config", func(e env) error { return e.c.Remove(p(e, ".git/config")) }},
		{"rmdir hooks", func(e env) error { return e.c.RemoveDirectory(p(e, ".git/hooks")) }},
		{"chmod config", func(e env) error { return e.c.Chmod(p(e, ".git/config"), 0o777) }},
		{"chmod via symlink", func(e env) error { return e.c.Chmod(p(e, "configlink"), 0o777) }},
		{"truncate config", func(e env) error { return e.c.Truncate(p(e, ".git/config"), 0) }},
		{"truncate via symlink", func(e env) error { return e.c.Truncate(p(e, "configlink"), 0) }},
		{"chtimes config", func(e env) error { return e.c.Chtimes(p(e, ".git/config"), time.Unix(0, 0), time.Unix(0, 0)) }},
		{"write outside", func(e env) error { return create(e, "../outside/secret") }},
		{"read outside", func(e env) error { return open(e, "../outside/secret", os.O_RDONLY) }},
		{"write through outside symlink", withOutsideLink(func(e env) error { return create(e, "out/secret") })},
		{"read through outside symlink", withOutsideLink(func(e env) error { return open(e, "out/secret", os.O_RDONLY) })},
	}
	for _, a := range attempts {
		t.Run(a.name, func(t *testing.T) {
			c, root := setupRooted(t, false)
			if err := a.f(env{c, root}); err == nil {
				t.Error("expected an error")
			}
			assertUnchanged(t, root)
		})
	}
}

// TestRootedNoopTimes checks the utimes request used by Lima's mountInotify.
func TestRootedNoopTimes(t *testing.T) {
	c, root := setupRooted(t, false)
	config := filepath.Join(root, ".git", "config")
	mtime := time.Unix(1_000_000_000, 0)
	if err := os.Chtimes(config, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	before := ctime(t, config)
	time.Sleep(10 * time.Millisecond)
	if err := c.Chtimes(config, mtime, mtime); err != nil {
		t.Fatalf("utimes to the current mtime: %v", err)
	}
	after, err := os.Lstat(config)
	if err != nil {
		t.Fatal(err)
	}
	if afterCtime := ctime(t, config); before != afterCtime || !after.ModTime().Equal(mtime) {
		t.Fatalf("file was modified: ctime %v -> %v, mtime %v", before, afterCtime, after.ModTime())
	}
	for _, times := range [][2]time.Time{
		{mtime, mtime.Add(time.Second)},
		{mtime.Add(time.Second), mtime},
		{mtime.Add(time.Second), mtime.Add(time.Second)},
	} {
		if err := c.Chtimes(config, times[0], times[1]); err == nil {
			t.Errorf("utimes to %v: expected an error", times)
		}
	}
	if err := c.Chtimes(filepath.Join(root, ".git", "missing"), mtime, mtime); err == nil {
		t.Error("utimes on a missing file: expected an error")
	}
	assertUnchanged(t, root)
}

func TestRootedReadonly(t *testing.T) {
	c, root := setupRooted(t, true)
	if _, err := c.Create(filepath.Join(root, "src", "new.go")); err == nil {
		t.Error("expected an error")
	}
	if err := c.Mkdir(filepath.Join(root, "dir")); err == nil {
		t.Error("expected an error")
	}
	if _, err := c.Stat(filepath.Join(root, "src", "main.go")); err != nil {
		t.Error(err)
	}
}

// TestRootedSymlinkSwapRace swaps a directory for a symlink to .git
// while the client keeps writing into it.
func TestRootedSymlinkSwapRace(t *testing.T) {
	c, root := setupRooted(t, false)
	d := filepath.Join(root, "d")
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			_ = os.RemoveAll(d)
			_ = os.Mkdir(d, 0o755)
			_ = os.RemoveAll(d)
			_ = os.Symlink(".git", d)
		}
	}()
	var created int
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if f, err := c.Create(filepath.Join(d, "pwn")); err == nil {
			f.Close()
			created++
		}
		_ = c.Mkdir(filepath.Join(d, "pwndir"))
		_ = c.Symlink("x", filepath.Join(d, "pwnlink"))
		_ = c.Chmod(filepath.Join(d, "config"), 0o777)
		_ = c.Truncate(filepath.Join(d, "config"), 0)
		_ = c.Remove(filepath.Join(d, "config"))
	}
	close(stop)
	wg.Wait()
	if created == 0 {
		t.Fatal("no write reached d while it was a directory, the race was not exercised")
	}
	assertUnchanged(t, root)
}

func TestSameName(t *testing.T) {
	for _, s := range []string{".git", ".GIT", ".Git", ".g" + zwnj + "it", bom + ".git", ".git" + zwj} {
		if !sameName(s, ".git") {
			t.Errorf("%q should match .git", s)
		}
	}
	for _, s := range []string{".gitignore", "git", ".git.", "x.git", ".gi"} {
		if sameName(s, ".git") {
			t.Errorf("%q should not match .git", s)
		}
	}
}
