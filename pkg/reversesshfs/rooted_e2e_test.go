//go:build linux

package reversesshfs

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/lima-vm/sshocker/pkg/util"
	"golang.org/x/sys/unix"
)

// TestRootedSSHFS mounts the rooted server with a real sshfs in slave mode.
// It runs only when $SSHFS names an sshfs binary and FUSE is usable.
func TestRootedSSHFS(t *testing.T) {
	sshfs := os.Getenv("SSHFS")
	if sshfs == "" {
		t.Skip("SSHFS is not set")
	}
	tmp := t.TempDir()
	root := filepath.Join(tmp, "root")
	mnt := filepath.Join(tmp, "mnt")
	for _, d := range []string{filepath.Join(root, "src"), mnt} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	git := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=a", "GIT_AUTHOR_EMAIL=a@a", "GIT_COMMITTER_NAME=a", "GIT_COMMITTER_EMAIL=a@a")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return string(out)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(root, "init", "-q")
	git(root, "add", ".")
	git(root, "commit", "-q", "-m", "init")

	cmd := exec.Command(sshfs, ":"+root, mnt, "-f", "-o", "slave")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	srv, h, err := newRootedServer(&util.RWC{ReadCloser: stdout, WriteCloser: stdin}, root, false, []string{".git"})
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() {
		_ = srv.Serve()
		_ = h.Close()
	}()
	t.Cleanup(func() {
		_ = exec.Command("fusermount3", "-u", mnt).Run()
		_ = cmd.Wait()
	})
	for i := 0; ; i++ {
		if _, err := os.Stat(filepath.Join(mnt, "src", "main.go")); err == nil {
			break
		}
		if i == 50 {
			t.Fatal("sshfs did not mount")
		}
		time.Sleep(100 * time.Millisecond)
	}

	sh := func(script string) error {
		c := exec.Command("sh", "-euc", script)
		c.Dir = mnt
		out, err := c.CombinedOutput()
		t.Logf("%s: %v: %s", script, err, out)
		return err
	}
	allowed := []string{
		"echo x > src/new.go && cat src/new.go",
		"mkdir -p a/b/c && touch a/b/c/f && rm -rf a",
		"sed -i s/main/foo/ src/main.go",
		"cp -a src src2 && rm -r src2",
		"chmod 600 src/new.go && truncate -s 0 src/new.go",
		"ln -s main.go src/link && readlink src/link && cat src/link && rm src/link",
		"mv src/new.go src/renamed.go && rm src/renamed.go",
		"df .",
		"cat .git/HEAD && ls .git/hooks",
		"git status --short",
		"git log --oneline",
	}
	for _, s := range allowed {
		if err := sh(s); err != nil {
			t.Errorf("%s: %v", s, err)
		}
	}
	denied := []string{
		"echo evil > .git/hooks/pre-commit",
		"echo evil >> .git/config",
		"mv .git x",
		"rm .git/HEAD",
		"chmod 777 .git/config",
		// Not @0: sshfs replaces a zero time with the current time.
		"touch -d @1 .git/config",
		"mkdir src/.git",
		"ln -s .git g && echo evil > g/hooks/post-checkout",
		"git add src",
		"git commit -q --allow-empty -m evil",
	}
	for _, s := range denied {
		if err := sh(s); err == nil {
			t.Errorf("%s: expected an error", s)
		}
	}
	if fi, err := os.Stat(filepath.Join(root, ".git", "config")); err != nil || fi.ModTime().Unix() == 1 {
		t.Errorf(".git/config times were changed: %v, %v", fi.ModTime(), err)
	}
	if _, err := os.Stat(filepath.Join(root, ".git", "hooks", "pre-commit")); err == nil {
		t.Error("hook was written")
	}
	if n := git(root, "rev-list", "--count", "HEAD"); n != "1\n" {
		t.Errorf("commit count changed: %q", n)
	}

	// Relay a host change the way Lima's guest agent does for mountInotify.
	for _, f := range []string{".git/HEAD", "src/main.go"} {
		if err := os.WriteFile(filepath.Join(root, f), []byte("ref: refs/heads/other\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		fi, err := os.Stat(filepath.Join(root, f))
		if err != nil {
			t.Fatal(err)
		}
		if !gotAttribEvent(t, filepath.Join(mnt, f), fi.ModTime()) {
			t.Errorf("%s: no IN_ATTRIB event in the mount", f)
		}
	}

	// Relay a host deletion: the hostagent calls ExpectRemove, then the guest agent removes the path.
	for _, tc := range []struct {
		name        string
		recreate    bool
		guestSeen   bool
		guestListed bool
		delay       time.Duration
	}{
		{name: "src/gone.txt", guestSeen: true},
		{name: "src/listed.txt", guestListed: true},
		{name: ".git/index.lock", guestSeen: true},
		{name: "src/later.txt", guestSeen: true, delay: 2 * time.Second},
		{name: "src/again.txt", guestSeen: true, recreate: true},
		{name: "src/unseen.txt"},
	} {
		hostPath := filepath.Join(root, tc.name)
		if err := os.WriteFile(hostPath, []byte("old"), 0o644); err != nil {
			t.Fatal(err)
		}
		if tc.guestSeen {
			if _, err := os.Stat(filepath.Join(mnt, tc.name)); err != nil {
				t.Fatal(err)
			}
		}
		if tc.guestListed {
			if _, err := os.ReadDir(filepath.Dir(filepath.Join(mnt, tc.name))); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.Remove(hostPath); err != nil {
			t.Fatal(err)
		}
		if tc.recreate {
			if err := os.WriteFile(hostPath, []byte("new"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		time.Sleep(tc.delay)
		h.expectRemove(hostPath)
		got := gotDeleteEvent(t, filepath.Join(mnt, tc.name))
		t.Logf("%s (seen: %v, listed: %v, delay: %v): IN_DELETE: %v", tc.name, tc.guestSeen, tc.guestListed, tc.delay, got)
		if tc.guestSeen && !got {
			t.Errorf("%s: no IN_DELETE event in the mount", tc.name)
		}
		if tc.recreate {
			if b, err := os.ReadFile(hostPath); err != nil || string(b) != "new" {
				t.Errorf("%s: the recreated file was modified: %q, %v", tc.name, b, err)
			}
		}
	}
}

func gotDeleteEvent(t *testing.T, p string) bool {
	t.Helper()
	fd, err := unix.InotifyInit1(unix.IN_CLOEXEC | unix.IN_NONBLOCK)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	if _, err := unix.InotifyAddWatch(fd, filepath.Dir(p), unix.IN_DELETE); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(p); err != nil {
		t.Logf("remove %s: %v", p, err)
		return false
	}
	buf := make([]byte, 4096)
	for range 20 {
		if n, err := unix.Read(fd, buf); err == nil && n > 0 {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

func gotAttribEvent(t *testing.T, p string, mtime time.Time) bool {
	t.Helper()
	fd, err := unix.InotifyInit1(unix.IN_CLOEXEC | unix.IN_NONBLOCK)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	if _, err := unix.InotifyAddWatch(fd, p, unix.IN_ATTRIB); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, mtime, mtime); err != nil {
		t.Logf("chtimes %s: %v", p, err)
		return false
	}
	buf := make([]byte, 4096)
	for range 20 {
		if n, err := unix.Read(fd, buf); err == nil && n > 0 {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}
