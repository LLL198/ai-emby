package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type managedFileRoot struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Path      string `json:"path"`
	HostPath  string `json:"hostPath"`
	ReadOnly  bool   `json:"readOnly"`
	Default   bool   `json:"default"`
	Available bool   `json:"available"`
}

type managedPathMapping struct {
	Host      string `json:"host"`
	Container string `json:"container"`
}

func managedPathMappings() []managedPathMapping {
	var mappings []managedPathMapping
	_ = json.Unmarshal([]byte(os.Getenv("FILE_MANAGER_PATH_MAPPINGS")), &mappings)
	return mappings
}

func managedWindowsPath(value string) bool {
	return len(value) > 2 && ((value[0] >= 'a' && value[0] <= 'z') || (value[0] >= 'A' && value[0] <= 'Z')) && value[1] == ':' && (value[2] == '\\' || value[2] == '/')
}

func managedResolvePath(value string) (container, host string, err error) {
	value = strings.TrimSpace(value)
	windows := managedWindowsPath(value)
	compare := strings.ReplaceAll(value, "\\", "/")
	if strings.ContainsAny(value, "\x00\r\n") {
		return "", "", errFilePath
	}
	for _, part := range strings.Split(compare, "/") {
		if part == ".." || part == "." {
			return "", "", errFilePath
		}
	}
	bestLength := -1
	for _, mapping := range managedPathMappings() {
		prefix := strings.TrimRight(strings.ReplaceAll(mapping.Host, "\\", "/"), "/")
		left, right := compare, prefix
		if managedWindowsPath(mapping.Host) {
			left, right = strings.ToLower(left), strings.ToLower(right)
		}
		if len(prefix) > bestLength && (left == right || strings.HasPrefix(left, right+"/")) {
			suffix := strings.TrimPrefix(compare[len(prefix):], "/")
			container, err = managedRootPath(filepath.Join(mapping.Container, suffix))
			if err != nil {
				return "", "", err
			}
			host = value
			bestLength = len(prefix)
		}
	}
	if bestLength >= 0 {
		return container, host, nil
	}
	if windows || strings.HasPrefix(value, "\\\\") {
		return "", "", errors.New("该 Windows 路径还没有挂载到 Docker；先把宿主目录挂载到容器，并配置路径映射后再添加")
	}
	container, err = managedRootPath(value)
	return container, "", err
}

func managedDisplayPath(path string) string {
	best := ""
	length := -1
	for _, mapping := range managedPathMappings() {
		base := filepath.Clean(mapping.Container)
		if len(base) > length && (path == base || strings.HasPrefix(path, base+"/")) {
			suffix := strings.TrimPrefix(strings.TrimPrefix(path, base), "/")
			best = strings.TrimRight(mapping.Host, "\\/")
			if suffix != "" {
				if managedWindowsPath(mapping.Host) {
					best += "\\" + strings.ReplaceAll(suffix, "/", "\\")
				} else {
					best += "/" + suffix
				}
			}
			length = len(base)
		}
	}
	if best != "" {
		return best
	}
	return path
}

func managedRootPath(value string) (string, error) {
	value = strings.TrimSpace(value)
	if !filepath.IsAbs(value) || strings.ContainsAny(value, "\x00\r\n") || strings.Contains(value, "\\") || len(value) > 4096 {
		return "", errors.New("请填写服务可访问的绝对目录路径，例如 /media、/movies；Docker 使用容器内路径")
	}
	for _, part := range strings.Split(value, "/") {
		if part == ".." || part == "." {
			return "", errFilePath
		}
	}
	return filepath.Clean(value), nil
}

func (a *App) managedFileRoots() ([]managedFileRoot, error) {
	roots := []managedFileRoot{{ID: "media", Name: "媒体目录", Path: filepath.Clean(fileRoot()), Default: true}}
	rows, err := a.db.Query("SELECT id,name,path,host_path,read_only FROM feature_file_roots ORDER BY created,id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var root managedFileRoot
		if err = rows.Scan(&root.ID, &root.Name, &root.Path, &root.HostPath, &root.ReadOnly); err != nil {
			return nil, err
		}
		roots = append(roots, root)
	}
	return roots, rows.Err()
}

func (a *App) managedFileRoot(key string) (managedFileRoot, error) {
	if key == "" || key == "media" {
		return managedFileRoot{ID: "media", Name: "媒体目录", Path: filepath.Clean(fileRoot()), Default: true}, nil
	}
	if len(key) != 32 {
		return managedFileRoot{}, errors.New("无效路径入口")
	}
	var root managedFileRoot
	err := a.db.QueryRow("SELECT id,name,path,host_path,read_only FROM feature_file_roots WHERE id=?", key).Scan(&root.ID, &root.Name, &root.Path, &root.HostPath, &root.ReadOnly)
	if errors.Is(err, sql.ErrNoRows) {
		return root, errors.New("这个路径入口已移除，请重新选择")
	}
	return root, err
}

func (a *App) managedFileRootsAPI(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/admin/features/file-roots/browse" {
		if !featureMethod(w, r, http.MethodGet) {
			return
		}
		path := r.URL.Query().Get("path")
		if path == "" {
			path = "/"
		}
		path, _, err := managedResolvePath(path)
		if err != nil {
			fail(w, 400, err.Error())
			return
		}
		root, err := scraperLibraryRoot(path)
		if err != nil {
			fileFail(w, err)
			return
		}
		defer root.Close()
		entries, err := namingEntries(root, ".")
		if err != nil {
			fileFail(w, err)
			return
		}
		directories := []M{}
		for _, entry := range entries {
			if entry.IsDir() && entry.Type()&os.ModeSymlink == 0 {
				directories = append(directories, M{"name": entry.Name(), "path": filepath.Join(path, entry.Name())})
			}
		}
		respond(w, M{"path": path, "displayPath": managedDisplayPath(path), "directories": directories})
		return
	}
	if r.URL.Path != "/admin/features/file-roots" {
		fail(w, 404, "路径接口不存在")
		return
	}
	if !featureMethod(w, r, http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete) {
		return
	}
	if r.Method == http.MethodGet {
		filesMu.RLock()
		defer filesMu.RUnlock()
		roots, err := a.managedFileRoots()
		if err != nil {
			featureError(w, err)
			return
		}
		for i := range roots {
			root, err := scraperLibraryRoot(roots[i].Path)
			roots[i].Available = err == nil
			if root != nil {
				root.Close()
			}
		}
		respond(w, M{"roots": roots, "environment": "server", "docker": fileDockerEnvironment()})
		return
	}
	filesMu.Lock()
	defer filesMu.Unlock()
	var request managedFileRoot
	if !body(w, r, &request) {
		return
	}
	if r.Method == http.MethodDelete {
		if request.ID == "media" || len(request.ID) != 32 {
			fail(w, 400, "不能移除默认媒体目录")
			return
		}
		if _, err := a.db.Exec("DELETE FROM feature_file_roots WHERE id=?", request.ID); err != nil {
			featureError(w, err)
			return
		}
		respond(w, M{"ok": true})
		return
	}
	path, host, err := managedResolvePath(request.Path)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	if path == filepath.Clean(fileRoot()) {
		fail(w, 409, "该路径已是默认媒体目录")
		return
	}
	request.Name = strings.TrimSpace(request.Name)
	if request.Name == "" {
		request.Name = filepath.Base(path)
		if request.Name == "/" {
			request.Name = "服务器根目录"
		}
	}
	if len(request.Name) > 160 || strings.ContainsAny(request.Name, "\x00\r\n") {
		fail(w, 400, "路径名称过长或无效")
		return
	}
	if r.Method == http.MethodPut {
		old, err := a.managedFileRoot(request.ID)
		if err != nil || old.Default {
			fail(w, 404, "路径入口不存在")
			return
		}
		if old.Path == path && host == "" {
			host = old.HostPath
		}
		// An offline path may still have its display name or access mode changed.
		if old.Path != path {
			root, err := scraperLibraryRoot(path)
			if err != nil {
				fileFail(w, err)
				return
			}
			root.Close()
		}
	} else {
		root, err := scraperLibraryRoot(path)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				fail(w, 404, "路径不存在；Docker 里只能选择已挂载到容器的目录")
			} else {
				fileFail(w, err)
			}
			return
		}
		root.Close()
		request.ID = id()
	}
	var duplicate int
	if err = a.db.QueryRow("SELECT count(*) FROM feature_file_roots WHERE path=? AND id<>?", path, request.ID).Scan(&duplicate); err != nil {
		featureError(w, err)
		return
	}
	if duplicate > 0 {
		fail(w, 409, "这个目录已经添加，请直接切换到对应路径")
		return
	}
	if r.Method == http.MethodPost {
		_, err = a.db.Exec("INSERT INTO feature_file_roots(id,name,path,host_path,read_only,created) VALUES(?,?,?,?,?,?)", request.ID, request.Name, path, host, request.ReadOnly, time.Now().UnixNano())
	} else {
		_, err = a.db.Exec("UPDATE feature_file_roots SET name=?,path=?,host_path=?,read_only=? WHERE id=?", request.Name, path, host, request.ReadOnly, request.ID)
	}
	if err != nil {
		featureError(w, err)
		return
	}
	request.Path = path
	request.HostPath = host
	request.Available = true
	respond(w, request)
}

func fileDockerEnvironment() bool { _, err := os.Stat("/.dockerenv"); return err == nil }
