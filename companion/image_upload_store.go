package main

import (
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
)

var userImageMIMEs = []string{"image/webp", "image/jpeg", "image/png"}

// 0x7a6d20. Hash the user identifier before using it as a file name.
func userImagePath(directory, userID, mime string) string {
	extension := ".webp"
	switch mime {
	case "image/jpeg":
		extension = ".jpg"
	case "image/png":
		extension = ".png"
	}
	root := os.Getenv("MEDIA_INFO_ROOT")
	if root == "" {
		root = "/app/data"
	}
	return filepath.Join(root, directory, digest(userID)+extension)
}

// 0x7a6f00.
func userImageFile(directory, userID string) (string, bool) {
	for _, mime := range userImageMIMEs {
		path := userImagePath(directory, userID, mime)
		if _, err := os.Stat(path); err == nil {
			return path, true
		}
	}
	return "", false
}

// 0x7a7040. The temporary file stays in the destination directory so the
// final rename is atomic; other image formats are removed after the rename.
func saveUserImage(directory, userID, mime string, data []byte) error {
	root := os.Getenv("MEDIA_INFO_ROOT")
	if root == "" {
		root = "/app/data"
	}
	dir := filepath.Join(root, directory)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	file, err := os.CreateTemp(dir, ".upload-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	err = file.Chmod(0o600)
	if err == nil {
		_, err = file.Write(data)
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	destination := userImagePath(directory, userID, mime)
	if err := os.Rename(file.Name(), destination); err != nil {
		return err
	}
	for _, otherMIME := range userImageMIMEs {
		path := userImagePath(directory, userID, otherMIME)
		if path == destination {
			continue
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

// 0x7a75c0.
func deleteUserImage(directory, userID string) error {
	for _, mime := range userImageMIMEs {
		if err := os.Remove(userImagePath(directory, userID, mime)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

// 0x7a7720. ServeContent implements HEAD, range and conditional requests.
func serveUserImage(w http.ResponseWriter, r *http.Request, path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	var prefix [512]byte
	n, _ := file.Read(prefix[:])
	w.Header().Set("Content-Type", http.DetectContentType(prefix[:n]))
	_, _ = file.Seek(0, io.SeekStart)
	http.ServeContent(w, r, info.Name(), info.ModTime(), file)
	return nil
}
