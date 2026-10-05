package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
)

func brandingPath(path string) bool {
	path = strings.TrimPrefix(strings.TrimRight(strings.ToLower(path), "/"), "/emby")
	return path == "/system/info/public" || path == "/system/info" || path == "/admin/enhancements" || path == "/manifest.json" || path == "/web/manifest.json"
}

func brandingResponse(db *sql.DB, response *http.Response) error {
	if response.StatusCode != http.StatusOK || response.Request == nil {
		return nil
	}
	path := strings.TrimRight(strings.ToLower(response.Request.URL.Path), "/")
	path = strings.TrimPrefix(path, "/emby")
	if !brandingPath(response.Request.URL.Path) {
		return nil
	}
	content, err := io.ReadAll(response.Body)
	if err != nil {
		return err
	}
	response.Body.Close()
	var data map[string]any
	if json.Unmarshal(content, &data) == nil && data != nil {
		name := "AI Emby"
		if db != nil {
			var saved string
			if db.QueryRowContext(response.Request.Context(), "SELECT v FROM settings WHERE k='server_name'").Scan(&saved) == nil && strings.TrimSpace(saved) != "" {
				name = strings.TrimSpace(saved)
			}
		}
		if strings.HasSuffix(path, "manifest.json") {
			data["name"], data["short_name"] = name, name
		} else {
			data["ServerName"] = name
			if strings.HasPrefix(path, "/system/") {
				data["ProductName"] = "AI Emby"
			}
		}
		content, err = json.Marshal(data)
		if err != nil {
			return err
		}
	}
	response.Body = io.NopCloser(bytes.NewReader(content))
	response.ContentLength = int64(len(content))
	response.Header.Set("Content-Length", strconv.Itoa(len(content)))
	response.Header.Set("Cache-Control", "no-store")
	response.Header.Del("ETag")
	return nil
}
