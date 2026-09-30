//go:build linux || darwin

package reversesshfs

import (
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/pkg/sftp"
	"golang.org/x/sys/unix"
)

var errDenied error = unix.EACCES

type rootedSys struct {
	rootFD int
}

func openRootedSys(localPath string) (rootedSys, error) {
	rootFD, err := unix.Open(localPath, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return rootedSys{}, &os.PathError{Op: "open", Path: localPath, Err: err}
	}
	return rootedSys{rootFD: rootFD}, nil
}

func (s rootedSys) close() error {
	return unix.Close(s.rootFD)
}

// slashPath returns the request path of the host path p.
func slashPath(p string) string {
	return path.Clean(filepath.ToSlash(p))
}

func startDirectory(rootPath string) string {
	return rootPath
}

func realPath(rootPath, p string) string {
	if !path.IsAbs(p) {
		p = path.Join(rootPath, p)
	}
	return path.Clean(p)
}

func writableName(string) bool {
	return true
}

// openParent returns a directory fd for the parent of rel, and the base name.
// The caller must close the fd.
func (h *rootedHandlers) openParent(rel string) (int, string, error) {
	fd, err := unix.Dup(h.rootFD)
	if err != nil {
		return -1, "", err
	}
	if rel == "." {
		return fd, ".", nil
	}
	dir, base := path.Split(rel)
	if dir != "" {
		for _, c := range strings.Split(strings.TrimSuffix(dir, "/"), "/") {
			if c == "" || c == "." || c == ".." {
				unix.Close(fd)
				return -1, "", unix.EACCES
			}
			next, err := unix.Openat(fd, c, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
			unix.Close(fd)
			if err != nil {
				return -1, "", err
			}
			fd = next
		}
	}
	return fd, base, nil
}

func (h *rootedHandlers) openFile(r *sftp.Request) (*os.File, error) {
	rel, err := h.writableRel(r.Filepath)
	if err != nil {
		return nil, err
	}
	pf := r.Pflags()
	flags := unix.O_NOFOLLOW | unix.O_CLOEXEC
	switch {
	case pf.Read && (pf.Write || pf.Append):
		flags |= unix.O_RDWR
	case pf.Write || pf.Append:
		flags |= unix.O_WRONLY
	default:
		flags |= unix.O_RDONLY
	}
	// O_APPEND is not set, as it conflicts with WriteAt; the client sends offsets.
	if pf.Creat {
		flags |= unix.O_CREAT
	}
	if pf.Trunc {
		flags |= unix.O_TRUNC
	}
	if pf.Excl {
		flags |= unix.O_EXCL
	}
	var mode uint32 = 0o644
	if r.AttrFlags().Permissions {
		mode = r.Attributes().Mode & 0o7777
	}
	dirfd, base, err := h.openParent(rel)
	if err != nil {
		return nil, err
	}
	defer unix.Close(dirfd)
	fd, err := unix.Openat(dirfd, base, flags, mode)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: r.Filepath, Err: err}
	}
	return os.NewFile(uintptr(fd), r.Filepath), nil
}

// inParent calls f with the parent directory fd and the base name of the writable path p.
func (h *rootedHandlers) inParent(op, p string, f func(dirfd int, base string) error) error {
	rel, err := h.writableRel(p)
	if err != nil {
		return err
	}
	dirfd, base, err := h.openParent(rel)
	if err != nil {
		return err
	}
	defer unix.Close(dirfd)
	if err := f(dirfd, base); err != nil {
		return &os.PathError{Op: op, Path: p, Err: err}
	}
	return nil
}

func (h *rootedHandlers) remove(r *sftp.Request) error {
	var flags int
	if r.Method == "Rmdir" {
		flags = unix.AT_REMOVEDIR
	}
	return h.inParent(strings.ToLower(r.Method), r.Filepath, func(dirfd int, base string) error {
		return unix.Unlinkat(dirfd, base, flags)
	})
}

func (h *rootedHandlers) mkdir(r *sftp.Request) error {
	var mode uint32 = 0o755
	if r.AttrFlags().Permissions {
		mode = r.Attributes().Mode & 0o7777
	}
	return h.inParent("mkdir", r.Filepath, func(dirfd int, base string) error {
		return unix.Mkdirat(dirfd, base, mode)
	})
}

// symlink has the link target in Filepath and the link path in Target.
func (h *rootedHandlers) symlink(r *sftp.Request) error {
	return h.inParent("symlink", r.Target, func(dirfd int, base string) error {
		return unix.Symlinkat(r.Filepath, dirfd, base)
	})
}

func (h *rootedHandlers) link(r *sftp.Request) error {
	return h.twoPaths(r.Filepath, r.Target, func(oldfd int, oldBase string, newfd int, newBase string) error {
		return unix.Linkat(oldfd, oldBase, newfd, newBase, 0)
	})
}

func (h *rootedHandlers) rename(r *sftp.Request, noReplace bool) error {
	return h.twoPaths(r.Filepath, r.Target, func(oldfd int, oldBase string, newfd int, newBase string) error {
		if noReplace {
			var st unix.Stat_t
			if err := unix.Fstatat(newfd, newBase, &st, unix.AT_SYMLINK_NOFOLLOW); err == nil {
				return os.ErrExist
			}
		}
		return unix.Renameat(oldfd, oldBase, newfd, newBase)
	})
}

func (h *rootedHandlers) twoPaths(oldPath, newPath string, f func(oldfd int, oldBase string, newfd int, newBase string) error) error {
	oldRel, err := h.writableRel(oldPath)
	if err != nil {
		return err
	}
	newRel, err := h.writableRel(newPath)
	if err != nil {
		return err
	}
	oldfd, oldBase, err := h.openParent(oldRel)
	if err != nil {
		return err
	}
	defer unix.Close(oldfd)
	newfd, newBase, err := h.openParent(newRel)
	if err != nil {
		return err
	}
	defer unix.Close(newfd)
	if err := f(oldfd, oldBase, newfd, newBase); err != nil {
		return &os.LinkError{Op: "rename", Old: oldPath, New: newPath, Err: err}
	}
	return nil
}

func (h *rootedHandlers) setstat(r *sftp.Request) error {
	rel, err := h.writableRel(r.Filepath)
	if err != nil {
		return err
	}
	dirfd, base, err := h.openParent(rel)
	if err != nil {
		return err
	}
	defer unix.Close(dirfd)
	flags := r.AttrFlags()
	attrs := r.Attributes()
	if flags.Size {
		fd, err := unix.Openat(dirfd, base, unix.O_WRONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
		if err != nil {
			return &os.PathError{Op: "truncate", Path: r.Filepath, Err: err}
		}
		err = unix.Ftruncate(fd, int64(attrs.Size))
		unix.Close(fd)
		if err != nil {
			return &os.PathError{Op: "truncate", Path: r.Filepath, Err: err}
		}
	}
	if flags.Permissions {
		if err := fchmodatNoFollow(dirfd, base, attrs.Mode&0o7777); err != nil {
			return &os.PathError{Op: "chmod", Path: r.Filepath, Err: err}
		}
	}
	if flags.UidGid {
		if err := unix.Fchownat(dirfd, base, int(attrs.UID), int(attrs.GID), unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return &os.PathError{Op: "chown", Path: r.Filepath, Err: err}
		}
	}
	if flags.Acmodtime {
		ts := []unix.Timespec{
			unix.NsecToTimespec(int64(attrs.Atime) * 1e9),
			unix.NsecToTimespec(int64(attrs.Mtime) * 1e9),
		}
		if err := unix.UtimesNanoAt(dirfd, base, ts, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return &os.PathError{Op: "chtimes", Path: r.Filepath, Err: err}
		}
	}
	return nil
}

// StatVFS implements sftp.StatVFSFileCmder.
func (h *rootedHandlers) StatVFS(r *sftp.Request) (*sftp.StatVFS, error) {
	if _, err := h.rel(r.Filepath); err != nil {
		return nil, err
	}
	var st unix.Statfs_t
	if err := unix.Fstatfs(h.rootFD, &st); err != nil {
		return nil, err
	}
	return statVFS(&st), nil
}
