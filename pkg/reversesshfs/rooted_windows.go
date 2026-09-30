package reversesshfs

import (
	"errors"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"github.com/pkg/sftp"
	"golang.org/x/sys/windows"
)

var errDenied error = windows.ERROR_ACCESS_DENIED

// The first four are not defined in x/sys/windows.
const (
	fileNameNormalized        = 0x0 // FILE_NAME_NORMALIZED
	volumeNameNone            = 0x4 // VOLUME_NAME_NONE
	fileLinkInformation       = 11
	fileRenameInformationEx   = 65
	fileAttributeReadonly     = windows.FILE_ATTRIBUTE_READONLY
	fileAttributeNormal       = windows.FILE_ATTRIBUTE_NORMAL
	fileDirectoryAccess       = windows.FILE_LIST_DIRECTORY | windows.FILE_TRAVERSE | windows.FILE_READ_ATTRIBUTES
	fileShareAll              = windows.FILE_SHARE_READ | windows.FILE_SHARE_WRITE | windows.FILE_SHARE_DELETE
	fileDispositionFlagsPOSIX = windows.FILE_DISPOSITION_DELETE | windows.FILE_DISPOSITION_POSIX_SEMANTICS | windows.FILE_DISPOSITION_IGNORE_READONLY_ATTRIBUTE
)

// rootedSys walks paths with NtCreateFile relative to directory handles.
// OBJ_DONT_REPARSE makes it fail on any reparse point (symlink, junction),
// like O_NOFOLLOW on each component.
//
// A name that passes the lexical check may still refer to a read-only entry,
// through its 8.3 short name (e.g. GIT~1 for .git, see CVE-2019-1353).
// So every handle that a write goes through is checked by its normalized
// name, in which the file system has replaced short names by long ones.
type rootedSys struct {
	localPath  string
	rootHandle windows.Handle
	rootName   string // normalized path of the root, without the volume and a trailing separator
}

func openRootedSys(localPath string) (rootedSys, error) {
	p, err := windows.UTF16PtrFromString(localPath)
	if err != nil {
		return rootedSys{}, err
	}
	h, err := windows.CreateFile(p, fileDirectoryAccess|windows.SYNCHRONIZE, fileShareAll, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return rootedSys{}, &os.PathError{Op: "open", Path: localPath, Err: err}
	}
	name, err := normalizedName(h)
	if err != nil {
		windows.CloseHandle(h)
		return rootedSys{}, &os.PathError{Op: "open", Path: localPath, Err: err}
	}
	return rootedSys{localPath: localPath, rootHandle: h, rootName: strings.TrimSuffix(name, `\`)}, nil
}

func (s rootedSys) close() error {
	return windows.CloseHandle(s.rootHandle)
}

// slashPath returns the request path of the host path p.
// sshfs sends the host path it was given, e.g. C:/Users/foo/bar,
// which the sftp request server turns into /C:/Users/foo/bar.
func slashPath(p string) string {
	return path.Clean("/" + filepath.ToSlash(filepath.Clean(p)))
}

func startDirectory(string) string {
	return "/"
}

func realPath(rootPath, p string) string {
	if filepath.VolumeName(p) != "" || strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\`) {
		return slashPath(p)
	}
	return path.Join(rootPath, filepath.ToSlash(p))
}

// writableName rejects backslashes, which are separators on Windows,
// and colons, which name NTFS streams (e.g. .git::$INDEX_ALLOCATION, see CVE-2019-1352).
func writableName(rel string) bool {
	return !strings.ContainsAny(rel, `\:`)
}

func normalizedName(h windows.Handle) (string, error) {
	buf := make([]uint16, windows.MAX_PATH)
	for {
		n, err := windows.GetFinalPathNameByHandle(h, &buf[0], uint32(len(buf)), fileNameNormalized|volumeNameNone)
		if err != nil {
			return "", err
		}
		if n < uint32(len(buf)) {
			return windows.UTF16ToString(buf[:n]), nil
		}
		buf = make([]uint16, n)
	}
}

// checkHandle denies fh when its normalized path has a read-only component, or is not under the root.
func (h *rootedHandlers) checkHandle(fh windows.Handle) error {
	name, err := normalizedName(fh)
	if err != nil {
		return err
	}
	if name == h.rootName {
		return nil
	}
	rel, ok := strings.CutPrefix(name, h.rootName+`\`)
	if !ok {
		return errDenied
	}
	for _, c := range strings.Split(rel, `\`) {
		c, _, _ = strings.Cut(c, ":")
		if h.isReadonlyName(c) {
			return errDenied
		}
	}
	return nil
}

func ntCreate(dir windows.Handle, name string, access, attrs, disposition, options, objFlags uint32) (windows.Handle, error) {
	objectName, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return windows.InvalidHandle, err
	}
	oa := &windows.OBJECT_ATTRIBUTES{
		RootDirectory: dir,
		ObjectName:    objectName,
		Attributes:    windows.OBJ_CASE_INSENSITIVE | objFlags,
	}
	oa.Length = uint32(unsafe.Sizeof(*oa))
	var fh windows.Handle
	err = windows.NtCreateFile(&fh, access|windows.SYNCHRONIZE, oa, &windows.IO_STATUS_BLOCK{}, nil, attrs, fileShareAll,
		disposition, options|windows.FILE_SYNCHRONOUS_IO_NONALERT|windows.FILE_OPEN_FOR_BACKUP_INTENT, 0, 0)
	if err != nil {
		return windows.InvalidHandle, ntError(err)
	}
	return fh, nil
}

func ntError(err error) error {
	s, ok := err.(windows.NTStatus)
	if !ok {
		return err
	}
	if s == windows.STATUS_REPARSE_POINT_ENCOUNTERED {
		return errDenied
	}
	return s.Errno()
}

// openParent returns a handle of the parent directory of rel, and the base name.
// The caller must close the handle.
func (h *rootedHandlers) openParent(rel string) (windows.Handle, string, error) {
	if rel == "." {
		return windows.InvalidHandle, "", errDenied
	}
	dir, base := path.Split(rel)
	dir = strings.TrimSuffix(dir, "/")
	if dir == "" {
		var dup windows.Handle
		p := windows.CurrentProcess()
		if err := windows.DuplicateHandle(p, h.rootHandle, p, &dup, 0, false, windows.DUPLICATE_SAME_ACCESS); err != nil {
			return windows.InvalidHandle, "", err
		}
		return dup, base, nil
	}
	for _, c := range strings.Split(dir, "/") {
		if c == "" || c == "." || c == ".." {
			return windows.InvalidHandle, "", errDenied
		}
	}
	fh, err := ntCreate(h.rootHandle, filepath.FromSlash(dir), fileDirectoryAccess, 0, windows.FILE_OPEN,
		windows.FILE_DIRECTORY_FILE, windows.OBJ_DONT_REPARSE)
	if err != nil {
		return windows.InvalidHandle, "", err
	}
	if err := h.checkHandle(fh); err != nil {
		windows.CloseHandle(fh)
		return windows.InvalidHandle, "", err
	}
	return fh, base, nil
}

// openChecked opens the writable path p, which must not be a reparse point unless options has FILE_OPEN_REPARSE_POINT.
// The caller must close both handles.
func (h *rootedHandlers) openChecked(p string, access, options uint32) (parent, fh windows.Handle, err error) {
	rel, err := h.writableRel(p)
	if err != nil {
		return windows.InvalidHandle, windows.InvalidHandle, err
	}
	parent, base, err := h.openParent(rel)
	if err != nil {
		return windows.InvalidHandle, windows.InvalidHandle, err
	}
	var objFlags uint32
	if options&windows.FILE_OPEN_REPARSE_POINT == 0 {
		objFlags = windows.OBJ_DONT_REPARSE
	}
	fh, err = ntCreate(parent, base, access, 0, windows.FILE_OPEN, options, objFlags)
	if err == nil {
		err = h.checkHandle(fh)
		if err != nil {
			windows.CloseHandle(fh)
		}
	}
	if err != nil {
		windows.CloseHandle(parent)
		return windows.InvalidHandle, windows.InvalidHandle, err
	}
	return parent, fh, nil
}

func (h *rootedHandlers) openFile(r *sftp.Request) (*os.File, error) {
	rel, err := h.writableRel(r.Filepath)
	if err != nil {
		return nil, err
	}
	parent, base, err := h.openParent(rel)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(parent)
	pf := r.Pflags()
	var access, options uint32
	switch {
	case pf.Read && (pf.Write || pf.Append):
		access = windows.FILE_GENERIC_READ | windows.FILE_GENERIC_WRITE
		options = windows.FILE_NON_DIRECTORY_FILE
	case pf.Write || pf.Append:
		access = windows.FILE_GENERIC_WRITE
		options = windows.FILE_NON_DIRECTORY_FILE
	default:
		access = windows.FILE_GENERIC_READ
	}
	// FILE_APPEND_DATA semantics are not requested, as the client sends offsets.
	disposition := uint32(windows.FILE_OPEN)
	switch {
	case pf.Creat && pf.Excl:
		disposition = windows.FILE_CREATE
	case pf.Creat:
		disposition = windows.FILE_OPEN_IF
	}
	var attrs uint32 = fileAttributeNormal
	if r.AttrFlags().Permissions && r.Attributes().Mode&0o200 == 0 {
		attrs = fileAttributeReadonly
	}
	fh, err := ntCreate(parent, base, access, attrs, disposition, options, windows.OBJ_DONT_REPARSE)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: r.Filepath, Err: err}
	}
	// Checked before truncating: base may be the short name of an existing read-only name.
	if err := h.checkHandle(fh); err != nil {
		windows.CloseHandle(fh)
		return nil, err
	}
	if pf.Trunc {
		if err := windows.Ftruncate(fh, 0); err != nil {
			windows.CloseHandle(fh)
			return nil, &os.PathError{Op: "truncate", Path: r.Filepath, Err: err}
		}
	}
	return os.NewFile(uintptr(fh), r.Filepath), nil
}

func (h *rootedHandlers) remove(r *sftp.Request) error {
	options := uint32(windows.FILE_OPEN_REPARSE_POINT)
	if r.Method == "Rmdir" {
		options |= windows.FILE_DIRECTORY_FILE
	}
	parent, fh, err := h.openChecked(r.Filepath, windows.DELETE|windows.FILE_READ_ATTRIBUTES, options)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(parent)
	defer windows.CloseHandle(fh)
	if r.Method == "Remove" {
		// As unlink, which removes a symlink to a directory but not a directory.
		var fi windows.ByHandleFileInformation
		if err := windows.GetFileInformationByHandle(fh, &fi); err != nil {
			return &os.PathError{Op: "remove", Path: r.Filepath, Err: err}
		}
		if fi.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0 && fi.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT == 0 {
			return &os.PathError{Op: "remove", Path: r.Filepath, Err: syscall.EISDIR}
		}
	}
	if err := deleteHandle(fh); err != nil {
		return &os.PathError{Op: strings.ToLower(r.Method), Path: r.Filepath, Err: err}
	}
	return nil
}

// deleteHandle is Deleteat of Go's internal/syscall/windows.
func deleteHandle(fh windows.Handle) error {
	flags := uint32(fileDispositionFlagsPOSIX)
	err := windows.NtSetInformationFile(fh, &windows.IO_STATUS_BLOCK{}, (*byte)(unsafe.Pointer(&flags)),
		uint32(unsafe.Sizeof(flags)), windows.FileDispositionInformationEx)
	switch err {
	case nil:
		return nil
	case windows.STATUS_INVALID_INFO_CLASS, windows.STATUS_INVALID_PARAMETER, windows.STATUS_NOT_SUPPORTED:
		// Older Windows, or a file system without POSIX semantics, such as FAT32.
		deleteFile := uint8(1)
		err = windows.NtSetInformationFile(fh, &windows.IO_STATUS_BLOCK{}, &deleteFile,
			uint32(unsafe.Sizeof(deleteFile)), windows.FileDispositionInformation)
	}
	return ntError(err)
}

func (h *rootedHandlers) mkdir(r *sftp.Request) error {
	rel, err := h.writableRel(r.Filepath)
	if err != nil {
		return err
	}
	parent, base, err := h.openParent(rel)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(parent)
	// A base that is the short name of an existing entry fails with STATUS_OBJECT_NAME_COLLISION.
	fh, err := ntCreate(parent, base, fileDirectoryAccess, fileAttributeNormal, windows.FILE_CREATE,
		windows.FILE_DIRECTORY_FILE, windows.OBJ_DONT_REPARSE)
	if err != nil {
		return &os.PathError{Op: "mkdir", Path: r.Filepath, Err: err}
	}
	return windows.CloseHandle(fh)
}

// symlink is not supported: creating a symlink needs a privilege, or the developer mode,
// and CreateSymbolicLink takes a path, so it would follow symlinks in the parent directories.
func (h *rootedHandlers) symlink(*sftp.Request) error {
	return sftp.ErrSSHFxOpUnsupported
}

func (h *rootedHandlers) link(r *sftp.Request) error {
	return h.twoPaths(r.Filepath, r.Target, windows.FILE_WRITE_ATTRIBUTES|windows.FILE_READ_ATTRIBUTES,
		windows.FILE_NON_DIRECTORY_FILE, true, func(src, newParent windows.Handle, newBase string) error {
			return setName(src, fileLinkInformation, 0, newParent, newBase)
		})
}

func (h *rootedHandlers) rename(r *sftp.Request, noReplace bool) error {
	return h.twoPaths(r.Filepath, r.Target, windows.DELETE|windows.FILE_READ_ATTRIBUTES, 0, noReplace,
		func(src, newParent windows.Handle, newBase string) error {
			var flags uint32 = windows.FILE_RENAME_POSIX_SEMANTICS | windows.FILE_RENAME_IGNORE_READONLY_ATTRIBUTE
			if !noReplace {
				flags |= windows.FILE_RENAME_REPLACE_IF_EXISTS
			}
			err := setName(src, fileRenameInformationEx, flags, newParent, newBase)
			switch err {
			case windows.STATUS_INVALID_INFO_CLASS, windows.STATUS_INVALID_PARAMETER, windows.STATUS_NOT_SUPPORTED:
				// Older Windows, or a file system without POSIX semantics.
				// The first byte of the flags is ReplaceIfExists.
				err = setName(src, windows.FileRenameInformation, flags&windows.FILE_RENAME_REPLACE_IF_EXISTS, newParent, newBase)
			}
			return err
		})
}

// twoPaths opens oldPath without following a symlink, and calls f with the parent directory handle and
// the base name of newPath.
func (h *rootedHandlers) twoPaths(oldPath, newPath string, access, options uint32, noReplace bool,
	f func(src, newParent windows.Handle, newBase string) error,
) error {
	newRel, err := h.writableRel(newPath)
	if err != nil {
		return err
	}
	oldParent, src, err := h.openChecked(oldPath, access, options|windows.FILE_OPEN_REPARSE_POINT)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(oldParent)
	defer windows.CloseHandle(src)
	newParent, newBase, err := h.openParent(newRel)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(newParent)
	// newBase may be the short name of an existing read-only name, which would be replaced.
	dst, err := ntCreate(newParent, newBase, windows.FILE_READ_ATTRIBUTES, 0, windows.FILE_OPEN, windows.FILE_OPEN_REPARSE_POINT, 0)
	switch {
	case err == nil:
		err = h.checkHandle(dst)
		windows.CloseHandle(dst)
		if err != nil {
			return err
		}
		if noReplace {
			return &os.LinkError{Op: "rename", Old: oldPath, New: newPath, Err: os.ErrExist}
		}
	case !errors.Is(err, os.ErrNotExist):
		return &os.LinkError{Op: "rename", Old: oldPath, New: newPath, Err: err}
	}
	if err := f(src, newParent, newBase); err != nil {
		return &os.LinkError{Op: "rename", Old: oldPath, New: newPath, Err: ntError(err)}
	}
	return nil
}

// fileNameInformation is FILE_RENAME_INFORMATION and FILE_LINK_INFORMATION.
// Flags is a BOOLEAN ReplaceIfExists for the non-Ex classes.
type fileNameInformation struct {
	Flags          uint32
	RootDirectory  windows.Handle
	FileNameLength uint32
	FileName       [1]uint16
}

// setName renames src, or creates a hard link of it, to name in the directory dir.
func setName(src windows.Handle, class, flags uint32, dir windows.Handle, name string) error {
	name16, err := windows.UTF16FromString(name)
	if err != nil {
		return err
	}
	name16 = name16[:len(name16)-1]
	size := unsafe.Offsetof(fileNameInformation{}.FileName) + uintptr(len(name16))*2
	buf := make([]uint64, (size+7)/8)
	info := (*fileNameInformation)(unsafe.Pointer(&buf[0]))
	info.Flags = flags
	info.RootDirectory = dir
	info.FileNameLength = uint32(len(name16) * 2)
	copy(unsafe.Slice(&info.FileName[0], len(name16)), name16)
	return windows.NtSetInformationFile(src, &windows.IO_STATUS_BLOCK{}, (*byte)(unsafe.Pointer(&buf[0])), uint32(size), class)
}

// fileBasicInformation is FILE_BASIC_INFORMATION. Zero values are left unchanged.
type fileBasicInformation struct {
	CreationTime, LastAccessTime, LastWriteTime, ChangeTime int64
	FileAttributes                                          uint32
	_                                                       uint32 // the C struct is 40 bytes on 386 too
}

func (h *rootedHandlers) setstat(r *sftp.Request) error {
	flags := r.AttrFlags()
	attrs := r.Attributes()
	if flags.UidGid {
		return &os.PathError{Op: "chown", Path: r.Filepath, Err: syscall.EWINDOWS}
	}
	access := uint32(windows.FILE_READ_ATTRIBUTES | windows.FILE_WRITE_ATTRIBUTES)
	if flags.Size {
		access |= windows.FILE_WRITE_DATA
	}
	parent, fh, err := h.openChecked(r.Filepath, access, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(parent)
	defer windows.CloseHandle(fh)
	if flags.Size {
		if err := windows.Ftruncate(fh, int64(attrs.Size)); err != nil {
			return &os.PathError{Op: "truncate", Path: r.Filepath, Err: err}
		}
	}
	if flags.Permissions {
		// As os.Chmod: only the owner write bit is used, for the read-only attribute.
		var fi windows.ByHandleFileInformation
		if err := windows.GetFileInformationByHandle(fh, &fi); err != nil {
			return &os.PathError{Op: "chmod", Path: r.Filepath, Err: err}
		}
		a := fi.FileAttributes &^ fileAttributeReadonly
		if attrs.Mode&0o200 == 0 {
			a |= fileAttributeReadonly
		}
		if a == 0 {
			a = fileAttributeNormal
		}
		info := fileBasicInformation{FileAttributes: a}
		err := windows.NtSetInformationFile(fh, &windows.IO_STATUS_BLOCK{}, (*byte)(unsafe.Pointer(&info)),
			uint32(unsafe.Sizeof(info)), windows.FileBasicInformation)
		if err != nil {
			return &os.PathError{Op: "chmod", Path: r.Filepath, Err: ntError(err)}
		}
	}
	if flags.Acmodtime {
		atime := windows.NsecToFiletime(int64(attrs.Atime) * 1e9)
		mtime := windows.NsecToFiletime(int64(attrs.Mtime) * 1e9)
		if err := windows.SetFileTime(fh, nil, &atime, &mtime); err != nil {
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
	p, err := windows.UTF16PtrFromString(h.localPath)
	if err != nil {
		return nil, err
	}
	var avail, total, free uint64
	if err := windows.GetDiskFreeSpaceEx(p, &avail, &total, &free); err != nil {
		return nil, &os.PathError{Op: "statvfs", Path: h.localPath, Err: err}
	}
	const bsize = 4096
	return &sftp.StatVFS{
		Bsize:   bsize,
		Frsize:  bsize,
		Blocks:  total / bsize,
		Bfree:   free / bsize,
		Bavail:  avail / bsize,
		Namemax: 255,
	}, nil
}
