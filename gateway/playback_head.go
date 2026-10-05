package main

import (
	"context"
	"database/sql"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

var playbackHeadClient = &http.Client{
	Timeout: 8 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

func strmPlaybackItem(path string) string {
	path = strings.ReplaceAll(path, "//", "/")
	for strings.HasPrefix(strings.ToLower(path), "/emby/") {
		path = path[len("/emby"):]
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	var route string
	if len(parts) == 3 && strings.EqualFold(parts[0], "videos") {
		route = parts[2]
	} else if len(parts) == 4 && strings.EqualFold(parts[0], "items") && strings.EqualFold(parts[2], "playback") {
		route = parts[3]
	} else {
		return ""
	}
	route = strings.ToLower(route)
	if route == "stream" || strings.HasPrefix(route, "stream.") || route == "original" || strings.HasPrefix(route, "original.") {
		return parts[1]
	}
	return ""
}

func serveSTRMHead(db *sql.DB, w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodHead || db == nil {
		return false
	}
	id := strmPlaybackItem(r.URL.Path)
	if id == "" {
		return false
	}
	var path string
	if db.QueryRowContext(r.Context(), "SELECT path FROM items WHERE id=$1", id).Scan(&path) != nil || !strings.EqualFold(filepath.Ext(path), ".strm") {
		return false
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	serveSTRMHeadFrom(w, r.WithContext(ctx), coreURL, playbackHeadClient)
	return true
}

// A GET-signed CDN link may reject HEAD. Authenticate through the normal core
// route, then obtain metadata with a one-byte GET without relaying its body.
func serveSTRMHeadFrom(w http.ResponseWriter, r *http.Request, core string, client *http.Client) {
	request, err := http.NewRequestWithContext(r.Context(), http.MethodHead, core+r.URL.RequestURI(), nil)
	if err != nil {
		sessionError(w, 502, "播放检查失败")
		return
	}
	request.Host = r.Host
	request.Header = r.Header.Clone()
	if request.Header.Get("X-Emby-Token") == "" {
		request.Header.Set("X-Emby-Token", featureGatewayToken(r))
	}
	response, err := client.Do(request)
	if err != nil {
		sessionError(w, 502, "播放服务暂时不可用")
		return
	}
	defer response.Body.Close()
	metadata, err := strmHeadMetadata(r, response, client)
	for key, values := range response.Header {
		w.Header()[key] = append([]string(nil), values...)
	}
	if err != nil || metadata == nil {
		w.WriteHeader(response.StatusCode)
		return
	}
	w.Header().Del("Location")
	w.Header().Del("Content-Type")
	w.Header().Del("Content-Length")
	w.Header().Del("Content-Range")
	w.Header().Del("Transfer-Encoding")
	w.Header().Del("Content-Encoding")
	w.Header().Del("Content-Disposition")
	w.Header().Del("ETag")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-AI-Emby-Head-Compatibility", "range-get")
	for key, values := range metadata {
		w.Header()[key] = values
	}
	w.WriteHeader(http.StatusOK)
}

func strmHeadMetadata(r *http.Request, core *http.Response, client *http.Client) (http.Header, error) {
	if !playbackRedirectStatus(core.StatusCode) || core.Header.Get("Location") == "" {
		return nil, nil
	}
	target, err := core.Request.URL.Parse(core.Header.Get("Location"))
	if err != nil {
		return nil, err
	}
	for hop := 0; hop < 5; hop++ {
		if !playbackHTTPURL(target) {
			return nil, nil
		}
		request, err := http.NewRequestWithContext(r.Context(), http.MethodGet, target.String(), nil)
		if err != nil {
			return nil, err
		}
		for _, key := range []string{"User-Agent", "Accept", "Accept-Language", "Icy-Metadata"} {
			if values := r.Header.Values(key); len(values) != 0 {
				request.Header[key] = append([]string(nil), values...)
			}
		}
		request.Header.Set("Range", "bytes=0-0")
		request.Header.Set("Accept-Encoding", "identity")
		response, err := client.Do(request)
		if err != nil {
			return nil, err
		}
		response.Body.Close()
		if playbackRedirectStatus(response.StatusCode) {
			target, err = response.Request.URL.Parse(response.Header.Get("Location"))
			if err != nil || response.Header.Get("Location") == "" {
				return nil, err
			}
			continue
		}
		if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusPartialContent {
			return nil, nil
		}
		contentType := strings.ToLower(response.Header.Get("Content-Type"))
		if strings.Contains(contentType, "json") || strings.HasPrefix(contentType, "text/") || strings.Contains(contentType, "xml") {
			return nil, nil
		}
		if encoding := response.Header.Get("Content-Encoding"); encoding != "" && !strings.EqualFold(encoding, "identity") {
			return nil, nil
		}
		length := response.ContentLength
		if response.StatusCode == http.StatusPartialContent {
			length = playbackTotalLength(response.Header.Get("Content-Range"))
			if length < 0 {
				return nil, nil
			}
		}
		headers := make(http.Header)
		for _, key := range []string{"Content-Type", "Accept-Ranges", "Last-Modified"} {
			if values := response.Header.Values(key); len(values) != 0 {
				headers[key] = append([]string(nil), values...)
			}
		}
		if headers.Get("Content-Type") == "" {
			headers.Set("Content-Type", "application/octet-stream")
		}
		if response.StatusCode == http.StatusPartialContent {
			headers.Set("Accept-Ranges", "bytes")
		}
		if length >= 0 {
			headers.Set("Content-Length", strconv.FormatInt(length, 10))
		}
		return headers, nil
	}
	return nil, nil
}

func playbackRedirectStatus(status int) bool {
	return status == 301 || status == 302 || status == 303 || status == 307 || status == 308
}

func playbackHTTPURL(target *url.URL) bool {
	return target != nil && (target.Scheme == "http" || target.Scheme == "https") && target.Host != "" && target.User == nil
}

func playbackTotalLength(contentRange string) int64 {
	rangeValue, total, ok := strings.Cut(contentRange, "/")
	if !ok || rangeValue != "bytes 0-0" {
		return -1
	}
	length, err := strconv.ParseInt(total, 10, 64)
	if err != nil || length <= 0 {
		return -1
	}
	return length
}
