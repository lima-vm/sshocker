package reversesshfs

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

func ctime(t *testing.T, p string) syscall.Timespec {
	t.Helper()
	fi, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Sys().(*syscall.Stat_t).Ctim
}

// TestFchmodatOPath runs the fallback for kernels without fchmodat2 (< 6.6) directly,
// as newer kernels only take it for symlinks.
func TestFchmodatOPath(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses permission checks")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "file"), []byte("orig"), 0); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "dir"), 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(dir, "dir"), 0o755) })
	if err := os.Symlink("file", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	dirfd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(dirfd)

	for _, name := range []string{"file", "dir"} {
		if err := fchmodatOPath(dirfd, name, 0o755); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if fi, err := os.Lstat(filepath.Join(dir, name)); err != nil || fi.Mode().Perm() != 0o755 {
			t.Fatalf("%s: %v, %v", name, fi, err)
		}
	}
	if err := fchmodatOPath(dirfd, "link", 0o600); !errors.Is(err, unix.ELOOP) {
		t.Fatalf("link: expected ELOOP, got %v", err)
	}
	if fi, err := os.Stat(filepath.Join(dir, "file")); err != nil || fi.Mode().Perm() != 0o755 {
		t.Fatalf("symlink target changed: %v, %v", fi, err)
	}
}
