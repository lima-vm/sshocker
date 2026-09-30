//go:build linux || darwin || windows

package reversesshfs

import (
	"errors"
	"io"
	"os"
	"path"
	"strings"

	"github.com/pkg/sftp"
)

// rootedHandlers serves only the files under rootPath.
//
// Reads go through os.Root, which follows symlinks but never outside the root.
// Writes open the parent directory without following any symlink,
// and are denied when any path component matches readonlyNames.
// Following no symlink on writes is what prevents the client from
// swapping a directory for a symlink into a read-only one.
// The OS-specific part is in rootedSys.
type rootedHandlers struct {
	rootPath      string // slash-separated, cleaned
	root          *os.Root
	readonly      bool
	readonlyNames []string
	rootedSys
}

func newRootedServer(rwc io.ReadWriteCloser, localPath string, readonly bool, readonlyNames []string) (*sftp.RequestServer, *rootedHandlers, error) {
	root, err := os.OpenRoot(localPath)
	if err != nil {
		return nil, nil, err
	}
	sys, err := openRootedSys(localPath)
	if err != nil {
		root.Close()
		return nil, nil, err
	}
	h := &rootedHandlers{
		rootPath:      slashPath(localPath),
		root:          root,
		readonly:      readonly,
		readonlyNames: readonlyNames,
		rootedSys:     sys,
	}
	handlers := sftp.Handlers{FileGet: h, FilePut: h, FileCmd: h, FileList: h}
	srv := sftp.NewRequestServer(rwc, handlers, sftp.WithStartDirectory(startDirectory(h.rootPath)))
	return srv, h, nil
}

func (h *rootedHandlers) Close() error {
	return errors.Join(h.root.Close(), h.rootedSys.close())
}

// rel maps a request path to a path relative to the root.
// The result has no "." or ".." component, except "." for the root itself.
func (h *rootedHandlers) rel(p string) (string, error) {
	if !path.IsAbs(p) {
		p = path.Join(h.rootPath, p)
	}
	p = path.Clean(p)
	if p == h.rootPath {
		return ".", nil
	}
	prefix := h.rootPath
	if prefix != "/" {
		prefix += "/"
	}
	if r, ok := strings.CutPrefix(p, prefix); ok {
		return r, nil
	}
	return "", errDenied
}

func (h *rootedHandlers) writableRel(p string) (string, error) {
	if h.readonly {
		return "", errDenied
	}
	r, err := h.rel(p)
	if err != nil {
		return "", err
	}
	if h.isReadonlyName(r) || !writableName(r) {
		return "", errDenied
	}
	return r, nil
}

func (h *rootedHandlers) isReadonlyName(rel string) bool {
	for _, c := range strings.Split(rel, "/") {
		for _, name := range h.readonlyNames {
			if sameName(c, name) {
				return true
			}
		}
	}
	return false
}

// sameName reports whether a file name may refer to the same entry as name
// on a case-insensitive (APFS, HFS+, NTFS) file system.
// The ignored code points are the ones listed in next_hfs_char() of git's utf8.c.
func sameName(s, name string) bool {
	s = strings.Map(func(r rune) rune {
		switch {
		case r >= 0x200c && r <= 0x200f, r >= 0x202a && r <= 0x202e, r >= 0x206a && r <= 0x206f, r == 0xfeff:
			return -1
		}
		return r
	}, s)
	return strings.EqualFold(s, name)
}

// Fileread implements sftp.FileReader.
func (h *rootedHandlers) Fileread(r *sftp.Request) (io.ReaderAt, error) {
	rel, err := h.rel(r.Filepath)
	if err != nil {
		return nil, err
	}
	return h.root.Open(rel)
}

// Filewrite implements sftp.FileWriter.
func (h *rootedHandlers) Filewrite(r *sftp.Request) (io.WriterAt, error) {
	return h.openFile(r)
}

// OpenFile implements sftp.OpenFileWriter.
func (h *rootedHandlers) OpenFile(r *sftp.Request) (sftp.WriterAtReaderAt, error) {
	return h.openFile(r)
}

// Filecmd implements sftp.FileCmder.
func (h *rootedHandlers) Filecmd(r *sftp.Request) error {
	switch r.Method {
	case "Setstat":
		if h.isNoopTimes(r) {
			return nil
		}
		return h.setstat(r)
	case "Rename":
		return h.rename(r, true)
	case "Link":
		return h.link(r)
	case "Remove", "Rmdir":
		return h.remove(r)
	case "Mkdir":
		return h.mkdir(r)
	case "Symlink":
		return h.symlink(r)
	}
	return sftp.ErrSSHFxOpUnsupported
}

// PosixRename implements sftp.PosixRenameFileCmder.
func (h *rootedHandlers) PosixRename(r *sftp.Request) error {
	return h.rename(r, false)
}

// isNoopTimes reports whether r only sets the access and modification times of a
// read-only name to its current modification time. Such a request is answered
// without touching the file, so that the guest kernel still emits IN_ATTRIB:
// this is how the guest agent relays host inotify events (mountInotify).
func (h *rootedHandlers) isNoopTimes(r *sftp.Request) bool {
	flags := r.AttrFlags()
	if h.readonly || flags.Size || flags.UidGid || flags.Permissions || !flags.Acmodtime {
		return false
	}
	rel, err := h.rel(r.Filepath)
	if err != nil || !h.isReadonlyName(rel) {
		return false
	}
	fi, err := h.root.Lstat(rel)
	if err != nil {
		return false
	}
	// SFTP v3 times are in seconds.
	mtime := uint32(fi.ModTime().Unix())
	attrs := r.Attributes()
	return attrs.Atime == mtime && attrs.Mtime == mtime
}

// Filelist implements sftp.FileLister.
func (h *rootedHandlers) Filelist(r *sftp.Request) (sftp.ListerAt, error) {
	rel, err := h.rel(r.Filepath)
	if err != nil {
		return nil, err
	}
	switch r.Method {
	case "List":
		f, err := h.root.Open(rel)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		fis, err := f.Readdir(-1)
		if err != nil {
			return nil, err
		}
		return listerAt(fis), nil
	case "Stat":
		fi, err := h.root.Stat(rel)
		if err != nil {
			return nil, err
		}
		return listerAt{fi}, nil
	}
	return nil, sftp.ErrSSHFxOpUnsupported
}

// Lstat implements sftp.LstatFileLister.
func (h *rootedHandlers) Lstat(r *sftp.Request) (sftp.ListerAt, error) {
	rel, err := h.rel(r.Filepath)
	if err != nil {
		return nil, err
	}
	fi, err := h.root.Lstat(rel)
	if err != nil {
		return nil, err
	}
	return listerAt{fi}, nil
}

// Readlink implements sftp.ReadlinkFileLister.
func (h *rootedHandlers) Readlink(p string) (string, error) {
	rel, err := h.rel(p)
	if err != nil {
		return "", err
	}
	return h.root.Readlink(rel)
}

// RealPath implements sftp.RealPathFileLister.
// It does not resolve symlinks, and does not access the file system.
func (h *rootedHandlers) RealPath(p string) (string, error) {
	return realPath(h.rootPath, p), nil
}

type listerAt []os.FileInfo

func (l listerAt) ListAt(ls []os.FileInfo, offset int64) (int, error) {
	if offset >= int64(len(l)) {
		return 0, io.EOF
	}
	n := copy(ls, l[offset:])
	if n < len(ls) {
		return n, io.EOF
	}
	return n, nil
}
