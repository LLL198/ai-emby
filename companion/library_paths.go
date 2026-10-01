package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// An explicit empty list keeps a library with no indexed directories.
func (a *App) libraryPaths(lib, fallback string) []string {
	var raw string
	if a.db.QueryRow("SELECT v FROM settings WHERE k=?", "library-paths:"+lib).Scan(&raw) == nil {
		var paths []string
		if json.Unmarshal([]byte(raw), &paths) == nil {
			return paths
		}
	}
	return []string{fallback}
}
func pathsOverlap(a, b string) bool {
	return a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}
func (a *App) libraryFolders(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" && r.Method != "DELETE" && r.Method != "PUT" {
		fail(w, 405, "method")
		return
	}
	var b struct{ ID, Path, OldPath string }
	if !body(w, r, &b) {
		return
	}
	a.libraryConfig.Lock()
	defer a.libraryConfig.Unlock()
	if r.Method != "POST" {
		if !a.scan.TryLock() {
			fail(w, 409, "扫描中，请完成后再修改目录")
			return
		}
		defer a.scan.Unlock()
	}
	var fallback string
	if a.db.QueryRow("SELECT path FROM libraries WHERE id=?", b.ID).Scan(&fallback) != nil {
		fail(w, 404, "媒体库不存在")
		return
	}
	paths := a.libraryPaths(b.ID, fallback)
	old := filepath.Clean(mediaPath(b.OldPath))
	if r.Method == "PUT" {
		found := false
		kept := []string{}
		for _, p := range paths {
			if p == old {
				found = true
			} else {
				kept = append(kept, p)
			}
		}
		if !found {
			fail(w, 404, "原目录不存在")
			return
		}
		paths = kept
	}
	path := filepath.Clean(mediaPath(b.Path))
	if r.Method == "POST" || r.Method == "PUT" {
		real, e := filepath.EvalSymlinks(path)
		if e != nil || !allowedMediaPath(real) {
			fail(w, 400, "请选择已挂载媒体目录下的现有目录")
			return
		}
		fi, e := os.Stat(real)
		if e != nil || !fi.IsDir() {
			fail(w, 400, "无效目录")
			return
		}
		path = real
		for _, lib := range a.libraries() {
			for _, p := range lib["Locations"].([]string) {
				if r.Method == "PUT" && lib["Id"] == b.ID && p == old {
					continue
				}
				if pathsOverlap(p, path) {
					fail(w, 409, "目录不能重复或互相包含")
					return
				}
			}
		}
		paths = append(paths, path)
	} else {
		found := false
		kept := []string{}
		for _, p := range paths {
			if p == path {
				found = true
			} else {
				kept = append(kept, p)
			}
		}
		if !found {
			fail(w, 404, "目录不属于此媒体库")
			return
		}
		paths = kept
	}
	raw, _ := json.Marshal(paths)
	tx, e := a.db.Begin()
	if e != nil {
		fail(w, 500, "索引事务失败")
		return
	}
	defer tx.Rollback()
	_, e = tx.Exec("INSERT INTO settings(k,v) VALUES(?,?) ON CONFLICT(k) DO UPDATE SET v=excluded.v", "library-paths:"+b.ID, string(raw))
	if e == nil && (r.Method == "DELETE" || r.Method == "PUT") {
		removed := path
		if r.Method == "PUT" {
			removed = old
		}
		_, e = tx.Exec("DELETE FROM items WHERE lib=? AND (path=? OR substr(path,1,length(?))=?)", b.ID, removed, removed+"/", removed+"/")
	}
	primary := "/media/.go-emby-empty-" + b.ID
	if len(paths) > 0 {
		primary = paths[0]
	}
	if e == nil {
		_, e = tx.Exec("UPDATE libraries SET path=? WHERE id=?", primary, b.ID)
	}
	if e == nil {
		_, e = tx.Exec("UPDATE libraries SET count=(SELECT count(*) FROM items WHERE lib=? AND kind IN ('Movie','Episode')) WHERE id=?", b.ID, b.ID)
	}
	if e != nil {
		fail(w, 500, "目录索引更新失败")
		return
	}
	if e = tx.Commit(); e != nil {
		fail(w, 500, "目录索引提交失败")
		return
	}
	if r.Method == "DELETE" {
		if e = a.cleanupMedia(); e != nil {
			fail(w, 500, "索引已移除，媒体信息清理失败")
			return
		}
	}
	respond(w, M{"ok": true, "Locations": paths, "queued": r.Method != "DELETE", "Message": "已加入队列，请在实时日志中查看"})
	if r.Method == "POST" || r.Method == "PUT" {
		go a.scanLibraryMode(b.ID, true)
	}
}
