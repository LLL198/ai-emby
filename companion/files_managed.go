package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

type managedFileEntry struct {
	fileEntry
	DirectoryPath string `json:"directoryPath"`
	AbsolutePath  string `json:"absolutePath"`
	Root          string `json:"root"`
	Scrape        bool   `json:"scrape"`
}

func (a *App) managedFilesAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		filesMu.RLock()
		defer filesMu.RUnlock()
	} else {
		filesMu.Lock()
		defer filesMu.Unlock()
	}
	config, err := a.managedFileRoot(r.URL.Query().Get("root"))
	if err != nil {
		fail(w, 404, err.Error())
		return
	}
	root, err := scraperLibraryRoot(config.Path)
	if err != nil {
		fileFail(w, err)
		return
	}
	defer root.Close()
	if r.Method != http.MethodGet && r.Method != http.MethodHead && config.ReadOnly {
		fail(w, 403, "这个路径设为只读，请在路径管理中调整后再修改文件")
		return
	}
	base := "/admin/features/local-files"
	switch r.URL.Path {
	case base:
		if r.Method == http.MethodGet {
			a.managedListFiles(w, r, root, config)
			return
		}
		if r.Method == http.MethodDelete {
			a.managedDeleteFiles(w, r, root, config)
			return
		}
	case base + "/download":
		if !featureMethod(w, r, http.MethodGet, http.MethodHead) {
			return
		}
		path, err := fileName(r.URL.Query().Get("path"))
		if err != nil {
			fileFail(w, err)
			return
		}
		info, err := fileCheck(root, path)
		if err != nil {
			fileFail(w, err)
			return
		}
		if !info.Mode().IsRegular() {
			fail(w, 400, "只能下载普通文件")
			return
		}
		file, err := root.Open(path)
		if err != nil {
			fileFail(w, err)
			return
		}
		defer file.Close()
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filepath.Base(path)}))
		http.ServeContent(w, r, filepath.Base(path), info.ModTime(), file)
		return
	case base + "/upload":
		if !featureMethod(w, r, http.MethodPost) {
			return
		}
		a.managedUpload(w, r, root, config)
		return
	case base + "/mkdir":
		if !featureMethod(w, r, http.MethodPost) {
			return
		}
		var request struct{ Path, Name string }
		if !body(w, r, &request) {
			return
		}
		path, err := fileName(request.Path)
		if err != nil {
			fileFail(w, err)
			return
		}
		name, err := managedFileBasename(request.Name)
		if err != nil {
			fileFail(w, err)
			return
		}
		info, err := fileCheck(root, path)
		if err != nil {
			fileFail(w, err)
			return
		}
		if !info.IsDir() {
			fail(w, 400, "目标不是目录")
			return
		}
		if err = root.Mkdir(filepath.Join(path, name), 0755); err != nil {
			fileFail(w, err)
			return
		}
		a.managedFilesChanged(config, []string{filepath.Join(path, name)})
		respond(w, M{"ok": true})
		return
	case base + "/rename", base + "/move":
		if !featureMethod(w, r, http.MethodPost) {
			return
		}
		a.managedMoveFiles(w, r, root, config)
		return
	}
	fail(w, 405, "不支持的文件操作")
}

func managedFileBasename(name string) (string, error) {
	path, err := fileName(name)
	if err != nil || path == "." || path != name || filepath.Base(path) != path || strings.Trim(name, " .") == "" || len(name) > 255 {
		return "", errFilePath
	}
	return name, nil
}

func (a *App) managedListFiles(w http.ResponseWriter, r *http.Request, root *os.Root, config managedFileRoot) {
	path, err := fileName(r.URL.Query().Get("path"))
	if err != nil {
		fileFail(w, err)
		return
	}
	info, err := fileCheck(root, path)
	if err != nil {
		fileFail(w, err)
		return
	}
	if !info.IsDir() {
		fail(w, 400, "路径不是目录")
		return
	}
	search := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("search")))
	if len(search) > 512 {
		fail(w, 400, "搜索词过长")
		return
	}
	scope, order, direction := r.URL.Query().Get("scope"), r.URL.Query().Get("sort"), r.URL.Query().Get("order")
	if scope != "" && scope != "current" && scope != "global" || order != "" && order != "name" && order != "time" && order != "size" || direction != "" && direction != "asc" && direction != "desc" {
		fail(w, 400, "无效排序或搜索范围")
		return
	}
	entries := []managedFileEntry{}
	visited := 0
	libraries := a.libraries()
	var walk func(string, int) error
	walk = func(dir string, depth int) error {
		if err := r.Context().Err(); err != nil {
			return err
		}
		if depth > 128 {
			return errors.New("目录层级过深，请缩小搜索范围")
		}
		file, err := root.Open(dir)
		if err != nil {
			return err
		}
		defer file.Close()
		for {
			items, readErr := file.ReadDir(512)
			for _, item := range items {
				visited++
				if visited > 500000 {
					return errors.New("目录过大，请缩小搜索范围")
				}
				if item.Type()&os.ModeSymlink != 0 {
					continue
				}
				child := filepath.Join(dir, item.Name())
				if search == "" || strings.Contains(strings.ToLower(item.Name()), search) {
					info, err := root.Lstat(child)
					if errors.Is(err, fs.ErrNotExist) {
						continue
					}
					if err != nil {
						return err
					}
					if info.Mode()&os.ModeSymlink != 0 || (!info.IsDir() && !info.Mode().IsRegular()) {
						continue
					}
					size := info.Size()
					if info.IsDir() {
						size = 0
					}
					full := filepath.Join(config.Path, child)
					directory := full
					if !info.IsDir() {
						directory = filepath.Dir(full)
					}
					canScrape := false
					if info.IsDir() || featureMediaExtension(item.Name()) {
						for _, library := range libraries {
							for _, location := range library["Locations"].([]string) {
								relative, err := filepath.Rel(location, full)
								if err == nil && filepath.IsLocal(relative) {
									canScrape = true
								}
							}
						}
					}
					entries = append(entries, managedFileEntry{fileEntry: fileEntry{Name: item.Name(), Path: "/" + child, IsDir: info.IsDir(), Size: size, Modified: info.ModTime()}, DirectoryPath: managedDisplayPath(directory), AbsolutePath: full, Root: config.ID, Scrape: canScrape})
				}
				if scope == "global" && search != "" && item.IsDir() {
					if err := walk(child, depth+1); err != nil {
						return err
					}
				}
			}
			if readErr != nil {
				if errors.Is(readErr, io.EOF) {
					return nil
				}
				return readErr
			}
		}
	}
	start := path
	if scope == "global" && search != "" {
		start = "."
	}
	if err = walk(start, 0); err != nil {
		fileFail(w, err)
		return
	}
	sort.Slice(entries, func(i, j int) bool {
		left, right := entries[i], entries[j]
		result := 0
		switch order {
		case "time":
			if left.Modified.Before(right.Modified) {
				result = -1
			} else if left.Modified.After(right.Modified) {
				result = 1
			}
		case "size":
			if left.Size < right.Size {
				result = -1
			} else if left.Size > right.Size {
				result = 1
			}
		default:
			result = strings.Compare(strings.ToLower(left.Name), strings.ToLower(right.Name))
		}
		if result == 0 {
			result = strings.Compare(left.Path, right.Path)
		}
		if direction == "desc" {
			return result > 0
		}
		return result < 0
	})
	respond(w, M{"entries": entries, "path": path, "root": config, "directoryPath": managedDisplayPath(filepath.Join(config.Path, path)), "total": len(entries)})
}

func (a *App) managedFilesAvailable(ctx context.Context, config managedFileRoot, paths []string) error {
	a.scraper.mu.Lock()
	busy := a.scraper.running || a.scraper.planning
	a.scraper.mu.Unlock()
	if busy {
		return errors.New("刮削任务正在运行，请完成后再修改文件")
	}
	if config.Default {
		if err := a.filesAvailable(ctx, paths); err != nil {
			return err
		}
	}
	fullPaths := []string{}
	for _, path := range paths {
		fullPaths = append(fullPaths, filepath.Join(config.Path, path))
	}
	libraries := a.libraries()
	a.scanner.mu.Lock()
	active := map[string]bool{}
	for id := range a.scanner.active {
		active[id] = true
	}
	a.scanner.mu.Unlock()
	for _, library := range libraries {
		for _, location := range library["Locations"].([]string) {
			for _, full := range fullPaths {
				if full == location || strings.HasPrefix(location, full+"/") {
					return errors.New("该目录包含媒体库根目录，请先在媒体库设置移除路径")
				}
				if (active[library["Id"].(string)] || library["Status"] == "scanning") && pathsOverlap(location, full) {
					return errors.New("这个目录的媒体库正在扫描，请完成后再操作")
				}
			}
		}
	}
	rows, err := a.db.QueryContext(ctx, "SELECT i.path FROM plays p JOIN items i ON i.id=p.item WHERE p.updated>?", time.Now().Add(-2*time.Minute).Unix())
	if err != nil {
		return errors.New("无法检查播放状态")
	}
	defer rows.Close()
	for rows.Next() {
		var playing string
		if rows.Scan(&playing) != nil {
			return errors.New("无法检查播放状态")
		}
		for _, full := range fullPaths {
			if pathsOverlap(full, playing) {
				return errors.New("文件被占用：正在播放，请停止播放后重试")
			}
		}
	}
	if err = rows.Err(); err != nil {
		return errors.New("无法检查播放状态")
	}
	if !config.Default && os.Getenv("FILE_BUSY_SOCKET") != "" {
		return errors.New("外部占用检查服务尚未支持这个新增路径")
	}
	return nil
}

func (a *App) managedFilesChanged(config managedFileRoot, paths []string) {
	for _, library := range a.libraries() {
		changed := false
		for _, location := range library["Locations"].([]string) {
			for _, path := range paths {
				if pathsOverlap(location, filepath.Join(config.Path, path)) {
					changed = true
					break
				}
			}
			if changed {
				break
			}
		}
		if changed {
			key := library["Id"].(string)
			if _, reserved := a.reserveConcurrentScan(key); reserved {
				go a.runConcurrentScan(key, false, false, nil)
			}
		}
	}
}

func (a *App) managedDeleteFiles(w http.ResponseWriter, r *http.Request, root *os.Root, config managedFileRoot) {
	var request struct{ Paths []string }
	if !body(w, r, &request) {
		return
	}
	if len(request.Paths) == 0 || len(request.Paths) > 100 {
		fail(w, 400, "请选择 1–100 个项目")
		return
	}
	paths := []string{}
	for _, value := range request.Paths {
		path, err := fileName(value)
		if err != nil {
			fileFail(w, err)
			return
		}
		if path == "." {
			fail(w, 400, "不能删除路径入口根目录")
			return
		}
		if _, err = fileCheck(root, path); err != nil {
			fileFail(w, err)
			return
		}
		for _, previous := range paths {
			if pathsOverlap(previous, path) {
				fail(w, 400, "删除路径不能重复或互相包含")
				return
			}
		}
		paths = append(paths, path)
	}
	if err := a.managedFilesAvailable(r.Context(), config, paths); err != nil {
		fail(w, 409, err.Error())
		return
	}
	deleted := []string{}
	for _, path := range paths {
		if err := root.RemoveAll(path); err != nil {
			a.managedFilesChanged(config, deleted)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(500)
			json.NewEncoder(w).Encode(M{"error": "删除失败，请刷新目录确认已完成的操作", "deleted": deleted})
			return
		}
		deleted = append(deleted, path)
	}
	a.managedFilesChanged(config, paths)
	respond(w, M{"ok": true, "deleted": deleted})
}

func managedRename(root *os.Root, old, next string, info fs.FileInfo) error {
	source, err := root.Open(filepath.Dir(old))
	if err != nil {
		return err
	}
	defer source.Close()
	target, err := root.Open(filepath.Dir(next))
	if err != nil {
		return err
	}
	defer target.Close()
	err = unix.Renameat2(int(source.Fd()), filepath.Base(old), int(target.Fd()), filepath.Base(next), unix.RENAME_NOREPLACE)
	if err == nil {
		return nil
	}
	if errors.Is(err, unix.EXDEV) {
		return errors.New("源目录与目标目录属于不同文件系统，请分别复制和删除")
	}
	if (errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EOPNOTSUPP)) && info.Mode().IsRegular() {
		if err = root.Link(old, next); err != nil {
			return err
		}
		if err = root.Remove(old); err != nil {
			_ = root.Remove(next)
			return err
		}
		return nil
	}
	return err
}

func (a *App) managedMoveFiles(w http.ResponseWriter, r *http.Request, root *os.Root, config managedFileRoot) {
	var request struct {
		OldPath, NewPath, Target string
		Paths                    []string
	}
	if !body(w, r, &request) {
		return
	}
	if strings.HasSuffix(r.URL.Path, "/rename") {
		request.Paths = []string{request.OldPath}
	}
	if len(request.Paths) == 0 || len(request.Paths) > 100 {
		fail(w, 400, "请选择 1–100 个项目")
		return
	}
	target := "."
	var err error
	if !strings.HasSuffix(r.URL.Path, "/rename") {
		target, err = fileName(request.Target)
		if err != nil {
			fileFail(w, err)
			return
		}
		info, err := fileCheck(root, target)
		if err != nil {
			fileFail(w, err)
			return
		}
		if !info.IsDir() {
			fail(w, 400, "目标不是目录")
			return
		}
	}
	paths := []string{}
	moves := []namingMove{}
	claimed := map[string]bool{}
	for _, value := range request.Paths {
		old, err := fileName(value)
		if err != nil {
			fileFail(w, err)
			return
		}
		if old == "." {
			fail(w, 400, "不能移动或改名路径入口根目录")
			return
		}
		info, err := fileCheck(root, old)
		if err != nil {
			fileFail(w, err)
			return
		}
		next := filepath.Join(target, filepath.Base(old))
		if strings.HasSuffix(r.URL.Path, "/rename") {
			if !info.IsDir() {
				fail(w, 400, "文件改名请使用刮削模块的规范命名")
				return
			}
			next, err = fileName(request.NewPath)
			if err != nil {
				fileFail(w, err)
				return
			}
			if filepath.Dir(old) != filepath.Dir(next) {
				fail(w, 400, "只能在当前目录重命名")
				return
			}
		}
		if next == "." || old == next || info.IsDir() && strings.HasPrefix(next, old+"/") {
			fail(w, 400, "目标与源相同或位于源目录内部")
			return
		}
		if _, err = root.Lstat(next); err == nil {
			fileFail(w, fs.ErrExist)
			return
		} else if !errors.Is(err, fs.ErrNotExist) {
			fileFail(w, err)
			return
		}
		if claimed[next] {
			fail(w, 409, "多个项目的目标名称重复")
			return
		}
		claimed[next] = true
		for _, move := range moves {
			if pathsOverlap(move.Old, old) {
				fail(w, 400, "选择项目不能互相包含")
				return
			}
		}
		paths = append(paths, old, next)
		moves = append(moves, namingMove{Old: old, New: next, Info: info})
	}
	if err = a.managedFilesAvailable(r.Context(), config, paths); err != nil {
		fail(w, 409, err.Error())
		return
	}
	completed := []string{}
	for _, move := range moves {
		if err = managedRename(root, move.Old, move.New, move.Info); err != nil {
			a.managedFilesChanged(config, completed)
			fail(w, 500, "移动或改名未全部完成，请刷新目录确认："+scraperFilesystemError("移动", err).Error())
			return
		}
		completed = append(completed, move.Old, move.New)
	}
	a.managedFilesChanged(config, completed)
	respond(w, M{"ok": true})
}

func (a *App) managedUpload(w http.ResponseWriter, r *http.Request, root *os.Root, config managedFileRoot) {
	path, err := fileName(r.URL.Query().Get("path"))
	if err != nil {
		fileFail(w, err)
		return
	}
	filename, err := url.PathUnescape(r.Header.Get("X-File-Name"))
	if err != nil {
		fileFail(w, errFilePath)
		return
	}
	name, err := managedFileBasename(filename)
	if err != nil {
		fileFail(w, err)
		return
	}
	info, err := fileCheck(root, path)
	if err != nil {
		fileFail(w, err)
		return
	}
	if !info.IsDir() {
		fail(w, 400, "目标不是目录")
		return
	}
	directory, err := root.OpenRoot(path)
	if err != nil {
		fileFail(w, err)
		return
	}
	defer directory.Close()
	if _, err = directory.Lstat(name); err == nil {
		fileFail(w, fs.ErrExist)
		return
	} else if !errors.Is(err, fs.ErrNotExist) {
		fileFail(w, err)
		return
	}
	temp := ".upload-" + id() + ".tmp"
	file, err := directory.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		fileFail(w, err)
		return
	}
	defer directory.Remove(temp)
	// Stream uploads; media files are not buffered in application memory.
	_, err = io.Copy(file, r.Body)
	if err == nil {
		err = r.Context().Err()
	}
	if err == nil {
		err = file.Sync()
	}
	stamp, statErr := file.Stat()
	closeErr := file.Close()
	if err == nil {
		err = statErr
	}
	if err == nil {
		err = closeErr
	}
	if err != nil {
		fileFail(w, err)
		return
	}
	if err = managedRename(directory, temp, name, stamp); err != nil {
		fileFail(w, err)
		return
	}
	a.managedFilesChanged(config, []string{filepath.Join(path, name)})
	respond(w, M{"ok": true})
}
