package reversesshfs

import (
	"os"
	"syscall"
	"testing"
)

func ctime(t *testing.T, p string) syscall.Timespec {
	t.Helper()
	fi, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Sys().(*syscall.Stat_t).Ctimespec
}
