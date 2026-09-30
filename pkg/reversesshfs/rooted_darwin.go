package reversesshfs

import (
	"github.com/pkg/sftp"
	"golang.org/x/sys/unix"
)

func statVFS(st *unix.Statfs_t) *sftp.StatVFS {
	return &sftp.StatVFS{
		Bsize:   uint64(st.Bsize),
		Frsize:  uint64(st.Bsize),
		Blocks:  st.Blocks,
		Bfree:   st.Bfree,
		Bavail:  st.Bavail,
		Files:   st.Files,
		Ffree:   st.Ffree,
		Favail:  st.Ffree,
		Flag:    uint64(st.Flags),
		Namemax: 1024,
	}
}

// fchmodatNoFollow never follows a symlink at base, and fails on one, as on Linux.
// If base is replaced by a symlink after the check, fchmodat changes the mode of the symlink itself.
func fchmodatNoFollow(dirfd int, base string, mode uint32) error {
	var st unix.Stat_t
	if err := unix.Fstatat(dirfd, base, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	if st.Mode&unix.S_IFMT == unix.S_IFLNK {
		return unix.ELOOP
	}
	return unix.Fchmodat(dirfd, base, mode, unix.AT_SYMLINK_NOFOLLOW)
}
