package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// Reconstructed from scraper_files.go functions in the running binary at
// 0x81c320-0x81d700. The renameat2 fallback is a local compatibility fix for
// mounted filesystems that return EINVAL/ENOSYS/EOPNOTSUPP for renameat2.

func (a *App) scraperWrite(libraryRoot string, target scraperTarget, item Item, data []byte, overwrite bool) error {
	artworkType, targetPath := target.Content, target.Path
	if !scraperAllowedTarget(target, item) || len(data) == 0 || len(data) > 0x1400000 {
		return errors.New("拒绝不支持的目标或文件内容")
	}
	if artworkType != "NFO" {
		encoded, err := scraperEncodeForPath(data, targetPath)
		if err != nil {
			return err
		}
		data = encoded
	}

	root, relativePath, err := scraperOpen(libraryRoot, targetPath)
	if err != nil {
		return err
	}
	defer root.Close()

	dir := filepath.Dir(relativePath)
	dirRoot, err := root.OpenRoot(dir)
	if err != nil {
		return scraperFilesystemError("无法打开目标目录", err)
	}
	defer dirRoot.Close()

	name := filepath.Base(relativePath)
	oldInfo, err := dirRoot.Lstat(name)
	if err == nil {
		if !oldInfo.Mode().IsRegular() {
			return errors.New("目标不是普通文件")
		}
		if !overwrite {
			return fs.ErrExist
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return errors.New("无法检查目标文件")
	}

	tempName := ".scraper-" + id() + ".tmp"
	tempPath := filepath.Join(dir, tempName)
	a.scraperSuppress(tempPath)
	tempFile, err := dirRoot.OpenFile(tempName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return scraperFilesystemError("创建临时文件失败", err)
	}
	tempRemoved := false
	defer func() {
		if !tempRemoved {
			_ = dirRoot.Remove(tempName)
		}
	}()

	_, writeErr := tempFile.Write(data)
	if writeErr == nil {
		writeErr = tempFile.Sync()
	}
	tempInfo, statErr := tempFile.Stat()
	closeErr := tempFile.Close()
	if writeErr != nil || statErr != nil || closeErr != nil {
		return errors.New("写入或同步临时文件失败")
	}

	if current, checkErr := dirRoot.Lstat(name); checkErr == nil {
		if !current.Mode().IsRegular() {
			return errors.New("目标已变为非普通文件")
		}
	} else if !errors.Is(checkErr, fs.ErrNotExist) {
		return errors.New("重新检查目标失败")
	}

	a.scraperSuppress(targetPath)
	targetSuppressed := true
	defer func() {
		if targetSuppressed {
			a.scraperWritten(targetPath, nil)
		}
	}()

	var renameErr error
	if !overwrite {
		// Keep the same directory-relative rename used by the binary. Some
		// FUSE/network mounts reject renameat2 even with flags=0.
		dirFile, openErr := dirRoot.Open(".")
		if openErr != nil {
			return scraperFilesystemError("原子写入失败", openErr)
		}
		renameErr = unix.Renameat2(int(dirFile.Fd()), tempName, int(dirFile.Fd()), name, 0)
		_ = dirFile.Close()
		if errors.Is(renameErr, unix.EINVAL) || errors.Is(renameErr, unix.ENOSYS) || errors.Is(renameErr, unix.EOPNOTSUPP) {
			renameErr = dirRoot.Rename(tempName, name)
		}
	} else {
		renameErr = dirRoot.Rename(tempName, name)
	}
	if errors.Is(renameErr, fs.ErrExist) {
		return fs.ErrExist
	}
	if renameErr != nil {
		return scraperFilesystemError("原子写入失败", renameErr)
	}
	tempRemoved = true
	targetSuppressed = false
	a.scraperWritten(targetPath, tempInfo)
	return nil
}

func scraperLibraryRoot(libraryPath string) (*os.Root, error) {
	fd, err := unix.Openat2(unix.AT_FDCWD, libraryPath, &unix.OpenHow{
		Flags:   uint64(unix.O_PATH | unix.O_CLOEXEC | unix.O_DIRECTORY),
		Resolve: unix.RESOLVE_NO_SYMLINKS,
	})
	if err != nil {
		return nil, err
	}
	defer unix.Close(fd)

	return os.OpenRoot(fmt.Sprintf("/proc/self/fd/%d", fd))
}

func scraperFilesystemError(operation string, err error) error {
	var pathErr *os.PathError
	if errors.As(err, &pathErr) && pathErr.Err != nil {
		err = pathErr.Err
	}

	switch {
	case errors.Is(err, unix.EPERM), errors.Is(err, fs.ErrPermission):
		return fmt.Errorf("%s：权限不足，请给容器运行 UID 的媒体目录设置读写和默认 ACL：%w", operation, err)
	case errors.Is(err, unix.EROFS):
		return fmt.Errorf("%s：媒体目录为只读挂载，请改为读写挂载：%w", operation, err)
	case errors.Is(err, unix.ENOSPC), errors.Is(err, unix.EDQUOT):
		return fmt.Errorf("%s：磁盘空间、inode 或配额不足：%w", operation, err)
	default:
		return fmt.Errorf("%s：%w", operation, err)
	}
}
