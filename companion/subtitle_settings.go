package main

import (
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
)

func (a *App) subtitleSettings() subtitleConfig {
	config := subtitleConfig{Directory: "/app/data/subtitles"}
	rows, err := a.db.Query("SELECT k,v FROM settings WHERE k IN ('subtitle_enabled','subtitle_auto_clean','subtitle_directory','subtitle_token','subtitle_save_beside_media')")
	if err != nil {
		return config
	}
	defer rows.Close()
	for rows.Next() {
		var key, value string
		if rows.Scan(&key, &value) != nil {
			continue
		}
		switch key {
		case "subtitle_enabled":
			config.Enabled = value == "true"
		case "subtitle_auto_clean":
			config.AutoClean = value == "true"
		case "subtitle_save_beside_media":
			config.SaveBesideMedia = value == "true"
		case "subtitle_directory":
			if value != "" {
				config.Directory = value
			}
		case "subtitle_token":
			config.Token = value
		}
	}
	config.TokenConfigured = config.Token != ""
	return config
}

func (a *App) subtitleSettingsAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodGet {
		respond(w, a.subtitleSettings())
		return
	}
	if r.Method != http.MethodPut {
		fail(w, 405, "PUT required")
		return
	}
	var input struct {
		Enabled, AutoClean, SaveBesideMedia *bool
		Directory, Token                    *string
		ClearToken                          bool
	}
	if !body(w, r, &input) {
		return
	}
	settings := map[string]string{}
	for key, value := range map[string]*bool{"subtitle_enabled": input.Enabled, "subtitle_auto_clean": input.AutoClean, "subtitle_save_beside_media": input.SaveBesideMedia} {
		if value != nil {
			settings[key] = strconv.FormatBool(*value)
		}
	}
	if input.Directory != nil {
		dir := filepath.Clean(strings.TrimSpace(*input.Directory))
		if !filepath.IsAbs(dir) || filepath.Dir(dir) == dir || strings.ContainsRune(dir, 0) {
			fail(w, 400, "字幕缓存目录必须为非根目录的绝对路径")
			return
		}
		settings["subtitle_directory"] = dir
	}
	if input.Token != nil {
		token := strings.TrimSpace(*input.Token)
		if token != "" {
			if len(token) != 32 || strings.IndexFunc(token, unicode.IsSpace) >= 0 {
				fail(w, 400, "ASSRT Token 应为 32 位")
				return
			}
			settings["subtitle_token"] = token
		}
	}
	if input.ClearToken {
		settings["subtitle_token"] = ""
	}
	tx, err := a.db.Begin()
	if err != nil {
		fail(w, 500, "保存失败")
		return
	}
	defer tx.Rollback()
	for key, value := range settings {
		if _, err = tx.Exec("INSERT INTO settings(k,v) VALUES(?,?) ON CONFLICT(k) DO UPDATE SET v=excluded.v", key, value); err != nil {
			fail(w, 500, "保存失败")
			return
		}
	}
	if tx.Commit() != nil {
		fail(w, 500, "保存失败")
		return
	}
	a.subtitles.mu.Lock()
	for _, flight := range a.subtitles.flights {
		if flight.cancel != nil {
			flight.cancel()
		}
	}
	a.subtitles.matches = nil
	a.subtitles.retry = nil
	a.subtitles.mu.Unlock()
	respond(w, a.subtitleSettings())
}
