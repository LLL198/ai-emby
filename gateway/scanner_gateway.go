package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httputil"
	"time"
)

func scanRequest(r *http.Request, base, path, method string, data []byte) (*http.Response, error) {
	request, err := http.NewRequestWithContext(r.Context(), method, base+path, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	request.Header = r.Header.Clone()
	if data != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	return (&http.Client{Timeout: 15 * time.Second}).Do(request)
}

func scanReadJSON(w http.ResponseWriter, r *http.Request, base, path string, target any) bool {
	response, err := get(r, base, path)
	if err != nil {
		http.Error(w, "扫描服务不可用", 502)
		return false
	}
	if response.StatusCode != 200 {
		relay(w, response)
		return false
	}
	defer response.Body.Close()
	if err = json.NewDecoder(io.LimitReader(response.Body, 16<<20)).Decode(target); err != nil {
		http.Error(w, "扫描服务响应无效", 502)
		return false
	}
	return true
}

func scanRespond(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(value)
}

func serveScanRoutes(w http.ResponseWriter, r *http.Request, companion *httputil.ReverseProxy) bool {
	path := r.URL.Path
	if path != "/admin/scan" && path != "/admin/scan-all" && path != "/admin/scan-control" && path != "/admin/scan-settings" && path != "/admin/scan/status" && path != "/admin/library-settings" && !(path == "/admin/libraries" && r.Method == http.MethodGet) {
		return false
	}
	// Validate administrator access before forwarding scanner requests.
	authorized, err := get(r, coreURL, "/admin/library-settings")
	if err != nil {
		http.Error(w, "核心服务不可用", 502)
		return true
	}
	if authorized.StatusCode != 200 {
		relay(w, authorized)
		return true
	}
	var settings map[string]any
	if err = json.NewDecoder(io.LimitReader(authorized.Body, 2<<20)).Decode(&settings); err != nil {
		authorized.Body.Close()
		http.Error(w, "媒体库设置响应无效", 502)
		return true
	}
	authorized.Body.Close()
	if path == "/admin/libraries" {
		var libraries []map[string]any
		if !scanReadJSON(w, r, coreURL, path, &libraries) {
			return true
		}
		var live struct {
			Libraries []struct {
				ID     string `json:"Id"`
				Status string
			}
		}
		if !scanReadJSON(w, r, scraperURL, "/admin/scan/status", &live) {
			return true
		}
		for _, status := range live.Libraries {
			for _, library := range libraries {
				if library["Id"] == status.ID {
					library["Status"] = status.Status
					library["Error"] = ""
				}
			}
		}
		scanRespond(w, libraries)
		return true
	}
	if path == "/admin/library-settings" {
		var local struct{ FileConcurrency int }
		if !scanReadJSON(w, r, scraperURL, "/admin/scan-settings", &local) {
			return true
		}
		if r.Method == http.MethodGet {
			settings["FileConcurrency"] = local.FileConcurrency
			scanRespond(w, settings)
			return true
		}
		if r.Method != http.MethodPut {
			http.Error(w, "GET or PUT required", 405)
			return true
		}
		var submitted map[string]json.RawMessage
		if err = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&submitted); err != nil {
			http.Error(w, "无效 JSON", 400)
			return true
		}
		if value, exists := submitted["FileConcurrency"]; exists {
			if err = json.Unmarshal(value, &local.FileConcurrency); err != nil || local.FileConcurrency < 1 || local.FileConcurrency > 32 {
				http.Error(w, "单库扫描并发必须为 1–32 的整数", 400)
				return true
			}
			delete(submitted, "FileConcurrency")
		}
		data, _ := json.Marshal(submitted)
		response, err := scanRequest(r, coreURL, path, http.MethodPut, data)
		if err != nil {
			http.Error(w, "保存媒体库设置失败", 502)
			return true
		}
		if response.StatusCode != 200 {
			relay(w, response)
			return true
		}
		var saved map[string]any
		err = json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(&saved)
		response.Body.Close()
		if err != nil {
			http.Error(w, "保存媒体库设置响应无效", 502)
			return true
		}
		data, _ = json.Marshal(local)
		response, err = scanRequest(r, scraperURL, "/admin/scan-settings", http.MethodPut, data)
		if err != nil {
			http.Error(w, "保存单库扫描并发失败", 502)
			return true
		}
		if response.StatusCode != 200 {
			relay(w, response)
			return true
		}
		response.Body.Close()
		saved["FileConcurrency"] = local.FileConcurrency
		scanRespond(w, saved)
		return true
	}
	if path == "/admin/scan-control" {
		if r.Method == http.MethodGet {
			var core, local struct{ Paused bool }
			if !scanReadJSON(w, r, coreURL, path, &core) || !scanReadJSON(w, r, scraperURL, path, &local) {
				return true
			}
			scanRespond(w, map[string]bool{"Paused": core.Paused || local.Paused})
			return true
		}
		if r.Method != http.MethodPut {
			http.Error(w, "GET or PUT required", 405)
			return true
		}
		data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
		if err != nil {
			http.Error(w, "无效请求", 400)
			return true
		}
		response, err := scanRequest(r, coreURL, path, http.MethodPut, data)
		if err != nil {
			http.Error(w, "核心扫描控制服务不可用", 502)
			return true
		}
		if response.StatusCode != 200 {
			relay(w, response)
			return true
		}
		response.Body.Close()
		response, err = scanRequest(r, scraperURL, path, http.MethodPut, data)
		if err != nil {
			http.Error(w, "并发扫描控制服务不可用", 502)
			return true
		}
		relay(w, response)
		return true
	}
	companion.ServeHTTP(w, r)
	return true
}
