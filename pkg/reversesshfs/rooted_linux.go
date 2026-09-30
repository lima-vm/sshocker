package reversesshfs

import (
	"errors"
	"strconv"

	"github.com/pkg/sftp"
	"golang.org/x/sys/unix"
)

func statVFS(st *unix.Statfs_t) *sftp.StatVFS {
	return &sftp.StatVFS{
		Bsize:   uint64(st.Bsize),
		Frsize:  uint64(st.Frsize),
		Blocks:  st.Blocks,
		Bfree:   st.Bfree,
		Bavail:  st.Bavail,
		Files:   st.Files,
		Ffree:   st.Ffree,
		Favail:  st.Ffree,
		Flag:    uint64(st.Flags),
		Namemax: uint64(st.Namelen),
	}
}

// fchmodatNoFollow never follows a symlink at base, and fails on one.
// Only fchmodat2 (kernel 6.6+) supports AT_SYMLINK_NOFOLLOW, and it fails with EOPNOTSUPP on a symlink.
func fchmodatNoFollow(dirfd int, base string, mode uint32) error {
	err := unix.Fchmodat(dirfd, base, mode, unix.AT_SYMLINK_NOFOLLOW)
	if !errors.Is(err, unix.EOPNOTSUPP) && !errors.Is(err, unix.ENOSYS) {
		return err
	}
	return fchmodatOPath(dirfd, base, mode)
}

// fchmodatOPath is the fallback of fchmodatNoFollow, as in glibc:
// fchmod does not work on an O_PATH fd, but chmod on its /proc/self/fd entry does,
// and unlike opening base for reading, needs no read permission.
func fchmodatOPath(dirfd int, base string, mode uint32) error {
	fd, err := unix.Openat(dirfd, base, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return err
	}
	if st.Mode&unix.S_IFMT == unix.S_IFLNK {
		return unix.ELOOP
	}
	return unix.Chmod("/proc/self/fd/"+strconv.Itoa(fd), mode)
}
