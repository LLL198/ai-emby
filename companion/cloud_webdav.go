package main

import (
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
)

func cloudWebDAVAddition(addition map[string]any) error {
	if skip, _ := addition["tls_insecure_skip_verify"].(bool); skip {
		return errors.New("WebDAV 播放需要受信任的 HTTPS 证书，请先配置有效证书")
	}
	address, _ := addition["address"].(string)
	address = strings.TrimSpace(address)
	u, err := url.Parse(address)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.ContainsAny(address, "\r\n\x00") {
		return errors.New("请填写完整的 HTTP 或 HTTPS WebDAV 地址，账号和密码请填在独立字段中")
	}
	addition["address"] = u.String()
	root, _ := addition["root_folder_path"].(string)
	root, err = cloudPath(strings.TrimSpace(root))
	if err != nil {
		return errors.New("WebDAV 根目录路径不合法，请填写 / 或 /电影 这样的目录路径")
	}
	addition["root_folder_path"] = root
	return nil
}

// Only the fixed loopback engine receives these requests. Its signed proxy
// handles provider authentication without exposing credentials to the player.
var cloudWebDAVHTTP = &http.Client{
	Transport: &http.Transport{
		Proxy:                 nil,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ResponseHeaderTimeout: 45 * time.Second,
		IdleConnTimeout:       90 * time.Second,
		MaxIdleConnsPerHost:   16,
		DisableCompression:    true,
	},
	CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
}

func (a *App) cloudWebDAVStream(w http.ResponseWriter, r *http.Request, m cloudMount, p string) {
	var file struct {
		Sign  string `json:"sign"`
		IsDir bool   `json:"is_dir"`
	}
	storagePath := cloudStoragePath(m, p)
	if err := cloudCall(r.Context(), "POST", "/api/fs/get", M{"path": storagePath}, &file, r.UserAgent()); err != nil {
		fail(w, 502, err.Error())
		return
	}
	if file.IsDir || file.Sign == "" {
		fail(w, 502, "WebDAV 文件不可读取，请检查挂载状态和目录权限")
		return
	}
	engine, _ := url.Parse(cloudEngineURL)
	engine.Path = "/p" + storagePath
	engine.RawQuery = url.Values{"sign": {file.Sign}, "d": {"1"}}.Encode()
	// GET also handles HEAD probes so Digest authentication uses the same method
	// that the WebDAV driver signed. Close the body after receiving its headers.
	request, err := http.NewRequestWithContext(r.Context(), http.MethodGet, engine.String(), nil)
	if err != nil {
		fail(w, 502, "WebDAV 播放请求无法创建")
		return
	}
	for _, header := range []string{"Range", "If-Range", "If-None-Match", "If-Modified-Since"} {
		if value := r.Header.Get(header); value != "" {
			request.Header.Set(header, value)
		}
	}
	request.Header.Set("User-Agent", r.UserAgent())
	request.Header.Set("Accept-Encoding", "identity")
	headProbe := r.Method == http.MethodHead && request.Header.Get("Range") == ""
	if headProbe {
		request.Header.Set("Range", "bytes=0-0")
	}
	response, err := cloudWebDAVHTTP.Do(request)
	if err != nil {
		if r.Context().Err() == nil {
			fail(w, 502, "WebDAV 文件读取失败，请检查服务连接和账号权限")
		}
		return
	}
	defer response.Body.Close()
	status := response.StatusCode
	if status != http.StatusOK && status != http.StatusPartialContent && status != http.StatusNotModified && status != http.StatusRequestedRangeNotSatisfiable {
		fail(w, 502, "WebDAV 文件读取失败，请检查账号、目录权限和服务状态")
		return
	}
	// Copy media headers only; engine errors, cookies and provider credentials
	// must never be relayed to clients.
	for _, header := range []string{"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges", "ETag", "Last-Modified", "Content-Encoding"} {
		if value := response.Header.Get(header); value != "" {
			w.Header().Set(header, value)
		}
	}
	if status == http.StatusRequestedRangeNotSatisfiable {
		w.Header().Del("Content-Length")
		fail(w, status, "请求的播放范围超出文件大小")
		return
	}
	if headProbe && status == http.StatusPartialContent {
		contentRange := response.Header.Get("Content-Range")
		if _, total, found := strings.Cut(contentRange, "/"); found {
			if size, err := strconv.ParseInt(total, 10, 64); err == nil && size > 0 {
				status = http.StatusOK
				w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
				w.Header().Del("Content-Range")
			}
		}
	}
	w.Header().Set("Content-Disposition", mime.FormatMediaType("inline", map[string]string{"filename": path.Base(p)}))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	if r.Method != http.MethodHead && status != http.StatusNotModified {
		_, _ = io.Copy(w, response.Body)
	}
}
