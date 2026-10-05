package main

import (
	"encoding/json"
	"net/http"
)

func (a *App) settingsSecret(w http.ResponseWriter, r *http.Request, user User, path string) {
	w.Header().Set("Cache-Control", "no-store")
	if !user.Admin || user.API {
		fail(w, 403, "administrator session required")
		return
	}
	if r.Method != http.MethodPost {
		fail(w, 405, "POST required")
		return
	}
	var key, field string
	switch path {
	case "/admin/tmdb/secret":
		key, field = "tmdb", "APIKey"
	case "/admin/subtitle/secret":
		key = "subtitle_token"
	case "/admin/telegram/secret":
		key, field = "telegram", "telegram_token"
	default:
		fail(w, 404, "not found")
		return
	}
	var raw string
	var config map[string]json.RawMessage
	var secret string
	if a.db.QueryRow("SELECT v FROM settings WHERE k=?", key).Scan(&raw) == nil {
		if field == "" {
			secret = raw
		} else if json.Unmarshal([]byte(raw), &config) == nil {
			_ = json.Unmarshal(config[field], &secret)
		}
	}
	respond(w, M{"Token": secret})
}
