package main

import (
	"os"
	"path/filepath"
	"strings"
)

// MEDIA_ROOTS is an explicit allowlist of container media mount points.
func mediaRoots() []string {
	raw := os.Getenv("MEDIA_ROOTS")
	if raw == "" {
		raw = "/media"
	}
	roots := []string{}
	for _, root := range strings.Split(raw, ":") {
		root = filepath.Clean(root)
		if filepath.IsAbs(root) && root != "/" {
			roots = append(roots, root)
		}
	}
	return roots
}
func allowedMediaPath(path string) bool {
	for _, root := range mediaRoots() {
		if path == root || strings.HasPrefix(path, root+"/") {
			return true
		}
	}
	return false
}
