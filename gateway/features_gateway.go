package main

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httputil"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type featureGatewayUser struct {
	ID     string
	Admin  bool
	Hidden bool
}

func featureGatewayToken(r *http.Request) string {
	for _, name := range []string{"X-Emby-Token", "X-MediaBrowser-Token"} {
		if v := r.Header.Get(name); v != "" {
			return v
		}
	}
	if v := r.URL.Query().Get("api_key"); v != "" {
		return v
	}
	if v := r.URL.Query().Get("X-Emby-Token"); v != "" {
		return v
	}
	auth := r.Header.Get("Authorization")
	if strings.HasPrefix(auth, "Bearer ") {
		return strings.TrimPrefix(auth, "Bearer ")
	}
	for _, name := range []string{"X-Emby-Authorization", "Authorization"} {
		for _, part := range strings.Split(r.Header.Get(name), ",") {
			k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
			if ok && k == "Token" {
				return strings.Trim(v, "\"")
			}
		}
	}
	return ""
}
func featureGatewayIdentity(db *sql.DB, r *http.Request) (featureGatewayUser, bool) {
	var user featureGatewayUser
	if db == nil {
		return user, false
	}
	digest := sha256.Sum256([]byte(featureGatewayToken(r)))
	var admin, hidden int
	err := db.QueryRowContext(r.Context(), "SELECT u.id,u.admin,u.can_view_hidden_libraries FROM users u JOIN tokens t ON t.user_id=u.id WHERE t.hash=$1 AND t.expires>$2", hex.EncodeToString(digest[:]), time.Now().Unix()).Scan(&user.ID, &admin, &hidden)
	if err != nil {
		err = db.QueryRowContext(r.Context(), "SELECT '',0,0 WHERE EXISTS(SELECT 1 FROM api_keys k WHERE k.hash=$1)", hex.EncodeToString(digest[:])).Scan(&user.ID, &admin, &hidden)
	}
	user.Admin = admin == 1
	user.Hidden = hidden == 1
	return user, err == nil
}
func featureGatewayAllowed(db *sql.DB, user featureGatewayUser, lib string) bool {
	if user.Admin || lib == "" {
		return true
	}
	var hidden int
	var raw string
	if db.QueryRow("SELECT l.hidden,COALESCE(p.data,'{}') FROM libraries l LEFT JOIN feature_library_policy p ON p.lib=l.id WHERE l.id=$1", lib).Scan(&hidden, &raw) != nil {
		return false
	}
	if hidden != 0 && !user.Hidden {
		return false
	}
	var p struct {
		RestrictUsers bool
		Users         []string
	}
	_ = json.Unmarshal([]byte(raw), &p)
	if !p.RestrictUsers {
		return true
	}
	for _, id := range p.Users {
		if id == user.ID {
			return true
		}
	}
	return false
}
func featureGatewayItemAllowed(db *sql.DB, user featureGatewayUser, id string) bool {
	if user.Admin {
		return true
	}
	var lib string
	err := db.QueryRow("SELECT lib FROM items WHERE id=$1 UNION ALL SELECT id FROM libraries WHERE id=$1 LIMIT 1", id).Scan(&lib)
	if err == sql.ErrNoRows {
		return true
	}
	return err == nil && featureGatewayAllowed(db, user, lib)
}

func serveFeatureRoutes(db *sql.DB, w http.ResponseWriter, r *http.Request, worker *httputil.ReverseProxy) bool {
	path := r.URL.Path
	if !strings.HasPrefix(path, "/admin/features/") && !strings.HasPrefix(path, "/features/") {
		return false
	}
	authPath := "/emby/Users/Me"
	if strings.HasPrefix(path, "/admin/") {
		authPath = "/admin/library-settings"
	}
	authorized, err := get(r, coreURL, authPath)
	if err != nil {
		sessionError(w, 502, "服务暂时不可用")
		return true
	}
	if authorized.StatusCode != 200 {
		relay(w, authorized)
		return true
	}
	authorized.Body.Close()
	if path == "/admin/features/tasks" {
		response, err := get(r, coreURL, "/admin/logs")
		if err == nil {
			if response.StatusCode == 200 {
				var data struct{ Entries []json.RawMessage }
				if json.NewDecoder(io.LimitReader(response.Body, 16<<20)).Decode(&data) == nil {
					payload, _ := json.Marshal(data)
					req, _ := http.NewRequestWithContext(r.Context(), "POST", scraperURL+"/admin/features/task-import", bytes.NewReader(payload))
					req.Header = r.Header.Clone()
					req.Header.Set("Content-Type", "application/json")
					if imported, e := (&http.Client{Timeout: 5 * time.Second}).Do(req); e == nil {
						imported.Body.Close()
					}
				}
			}
			response.Body.Close()
		}
	}
	worker.ServeHTTP(w, r)
	return true
}

func featureEnforceAccess(db *sql.DB, w http.ResponseWriter, r *http.Request) bool {
	if strings.HasPrefix(r.URL.Path, "/admin/") || strings.HasPrefix(r.URL.Path, "/web/") {
		return false
	}
	user, ok := featureGatewayIdentity(db, r)
	if !ok || user.Admin {
		return false
	}
	path := strings.TrimPrefix(r.URL.Path, "/emby")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	id := ""
	for i, p := range parts {
		switch strings.ToLower(p) {
		case "items", "videos", "audio", "shows", "favoriteitems", "playeditems":
			if i+1 < len(parts) {
				id = parts[i+1]
			}
		}
	}
	if id != "" && !featureGatewayItemAllowed(db, user, id) {
		sessionError(w, 403, "无权访问这个媒体库")
		return true
	}
	for _, param := range []string{"ParentId", "SeriesId", "SeasonId", "Ids"} {
		for _, id := range strings.Split(r.URL.Query().Get(param), ",") {
			if id != "" && !featureGatewayItemAllowed(db, user, id) {
				sessionError(w, 403, "无权访问这个媒体库")
				return true
			}
		}
	}
	return false
}

func featureFilterResponse(db *sql.DB, response *http.Response) error {
	if response.StatusCode != 200 || !strings.Contains(response.Header.Get("Content-Type"), "json") || response.Request == nil {
		return nil
	}
	path := strings.ToLower(response.Request.URL.Path)
	if !strings.Contains(path, "/items") && !strings.Contains(path, "/views") && !strings.Contains(path, "/shows") && !strings.Contains(path, "/playbackinfo") {
		return nil
	}
	user, ok := featureGatewayIdentity(db, response.Request)
	if !ok {
		return nil
	}
	data, err := io.ReadAll(response.Body)
	if err != nil {
		return err
	}
	response.Body.Close()
	var value any
	if json.Unmarshal(data, &value) != nil {
		response.Body = io.NopCloser(bytes.NewReader(data))
		return nil
	}
	var overlay func(any) any
	overlay = func(value any) any {
		switch v := value.(type) {
		case []any:
			out := make([]any, 0, len(v))
			for _, entry := range v {
				if m, ok := entry.(map[string]any); ok {
					if id, _ := m["Id"].(string); id != "" && !featureGatewayItemAllowed(db, user, id) {
						continue
					}
				}
				out = append(out, overlay(entry))
			}
			return out
		case map[string]any:
			if id, _ := v["Id"].(string); id != "" {
				var raw string
				if db.QueryRow("SELECT data FROM feature_metadata WHERE item=$1", id).Scan(&raw) == nil {
					var m map[string]any
					_ = json.Unmarshal([]byte(raw), &m)
					for source, target := range map[string]string{"Title": "Name", "Plot": "Overview", "Year": "ProductionYear", "Season": "ParentIndexNumber", "Episode": "IndexNumber", "Genres": "Genres", "Countries": "ProductionLocations", "Tags": "Tags", "Rating": "CommunityRating", "OriginalTitle": "OriginalTitle"} {
						if value, ok := m[source]; ok {
							v[target] = value
						}
					}
					if tmdb, ok := m["TMDB"].(string); ok && tmdb != "" {
						providers, _ := v["ProviderIds"].(map[string]any)
						if providers == nil {
							providers = map[string]any{}
						}
						providers["Tmdb"] = tmdb
						v["ProviderIds"] = providers
					}
				}
				var count int
				_ = db.QueryRow("SELECT count(*) FROM feature_artwork WHERE item=$1 AND kind='Primary'", id).Scan(&count)
				if count > 0 {
					v["PrimaryImageTag"] = "custom"
					tags, _ := v["ImageTags"].(map[string]any)
					if tags == nil {
						tags = map[string]any{}
					}
					tags["Primary"] = "custom"
					v["ImageTags"] = tags
				}
			}
			for key, entry := range v {
				if key == "Items" {
					before, _ := entry.([]any)
					filtered := overlay(entry)
					after, _ := filtered.([]any)
					v[key] = filtered
					if total, ok := v["TotalRecordCount"].(float64); ok {
						v["TotalRecordCount"] = total - float64(len(before)-len(after))
					}
				} else if key == "MediaSources" {
					v[key] = overlay(entry)
				}
			}
			return v
		}
		return value
	}
	value = overlay(value)
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	response.Body = io.NopCloser(bytes.NewReader(encoded))
	response.ContentLength = int64(len(encoded))
	response.Header.Del("Content-Length")
	response.Header.Del("ETag")
	return nil
}

func serveFeatureImage(db *sql.DB, w http.ResponseWriter, r *http.Request) bool {
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/emby"), "/"), "/")
	if len(parts) < 4 || strings.ToLower(parts[0]) != "items" || strings.ToLower(parts[2]) != "images" {
		return false
	}
	kind := parts[3]
	var count int
	if db == nil || db.QueryRow("SELECT count(*) FROM feature_artwork WHERE item=$1 AND kind=$2", parts[1], kind).Scan(&count) != nil || count == 0 {
		return false
	}
	authorized, err := get(r, coreURL, "/emby/Users/Me")
	if err != nil {
		sessionError(w, 502, "服务暂时不可用")
		return true
	}
	if authorized.StatusCode != 200 {
		relay(w, authorized)
		return true
	}
	authorized.Body.Close()
	response, err := get(r, scraperURL, "/features/artwork?ID="+parts[1]+"&Kind="+kind)
	if err != nil {
		sessionError(w, 502, "图片暂时不可用")
		return true
	}
	relay(w, response)
	return true
}

func serveFeatureLocalStream(db *sql.DB, w http.ResponseWriter, r *http.Request) bool {
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/emby"), "/"), "/")
	if len(parts) < 3 || !strings.EqualFold(parts[0], "Videos") || !strings.HasPrefix(strings.ToLower(parts[2]), "stream") {
		return false
	}
	var path, root, lib string
	if db == nil || db.QueryRow("SELECT i.path,l.path,i.lib FROM items i JOIN libraries l ON l.id=i.lib WHERE i.id=$1", parts[1]).Scan(&path, &root, &lib) != nil || strings.EqualFold(filepath.Ext(path), ".strm") {
		return false
	}
	authorized, err := scanRequest(r, coreURL, "/emby/Items/"+parts[1]+"/PlaybackInfo", http.MethodPost, []byte("{}"))
	if err != nil {
		sessionError(w, 502, "服务暂时不可用")
		return true
	}
	if authorized.StatusCode != 200 {
		relay(w, authorized)
		return true
	}
	authorized.Body.Close()
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		sessionError(w, 404, "媒体文件不存在")
		return true
	}
	roots := []string{root}
	var raw string
	if db.QueryRow("SELECT v FROM settings WHERE k=$1", "library-paths:"+lib).Scan(&raw) == nil {
		var saved []string
		if json.Unmarshal([]byte(raw), &saved) == nil {
			roots = saved
		}
	}
	inside := false
	for _, directory := range roots {
		if rel, e := filepath.Rel(directory, real); e == nil && filepath.IsLocal(rel) {
			inside = true
			break
		}
	}
	if !inside {
		sessionError(w, 403, "媒体路径不可访问")
		return true
	}
	if strings.EqualFold(filepath.Ext(real), ".iso") {
		sessionError(w, 415, "光盘文件需要开启转码缓存播放")
		return true
	}
	if info, err := os.Stat(real); err != nil || !info.Mode().IsRegular() {
		sessionError(w, 404, "媒体文件不存在")
		return true
	}
	http.ServeFile(w, r, real)
	return true
}

func featureNetworkGate(db *sql.DB, w http.ResponseWriter, r *http.Request) bool {
	if db == nil {
		return false
	}
	var raw string
	if db.QueryRow("SELECT v FROM settings WHERE k='feature:network'").Scan(&raw) != nil {
		return false
	}
	var c struct{ AllowedHosts, AllowedOrigins []string }
	_ = json.Unmarshal([]byte(raw), &c)
	if target, ok := w.(*featureNetworkResponse); ok && r.Header.Get("Origin") != "" && len(c.AllowedOrigins) > 0 {
		target.configured = true
		target.origin = r.Header.Get("Origin")
		for _, origin := range c.AllowedOrigins {
			if origin == target.origin {
				target.allowed = true
			}
		}
	}
	if len(c.AllowedHosts) > 0 {
		host := strings.ToLower(r.Host)
		allowed := strings.HasPrefix(host, "127.0.0.1:") || host == "localhost" || strings.HasPrefix(host, "localhost:")
		for _, h := range c.AllowedHosts {
			if strings.EqualFold(host, h) {
				allowed = true
			}
		}
		if !allowed {
			sessionError(w, 403, "访问主机未获允许")
			return true
		}
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		for _, v := range c.AllowedOrigins {
			if v == origin {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Vary", "Origin")
				w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Emby-Token, X-Emby-Authorization")
				w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, POST, PUT, DELETE, OPTIONS")
				if r.Method == "OPTIONS" {
					w.WriteHeader(204)
					return true
				}
			}
		}
	}
	return false
}

type featureNetworkResponse struct {
	http.ResponseWriter
	written, configured, allowed bool
	origin                       string
}

func (w *featureNetworkResponse) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *featureNetworkResponse) WriteHeader(status int) {
	if w.written {
		return
	}
	w.written = true
	if w.configured {
		if w.allowed {
			w.Header().Set("Access-Control-Allow-Origin", w.origin)
		} else {
			w.Header().Del("Access-Control-Allow-Origin")
		}
	}
	w.ResponseWriter.WriteHeader(status)
}
func (w *featureNetworkResponse) Write(data []byte) (int, error) {
	if !w.written {
		w.WriteHeader(200)
	}
	return w.ResponseWriter.Write(data)
}
func (w *featureNetworkResponse) Flush() {
	if !w.written {
		w.WriteHeader(200)
	}
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

type featureEventResponse struct {
	http.ResponseWriter
	status int
}

func (w *featureEventResponse) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}
func (w *featureEventResponse) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.status = 200
	}
	return w.ResponseWriter.Write(data)
}
func serveFeaturePlaybackEvent(w http.ResponseWriter, r *http.Request, core *httputil.ReverseProxy) bool {
	path := strings.ToLower(strings.TrimPrefix(r.URL.Path, "/emby"))
	if r.Method != "POST" || !strings.HasPrefix(path, "/sessions/playing") {
		return false
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, (1<<20)+1))
	if err != nil || len(data) > 1<<20 {
		sessionError(w, 400, "播放事件无效")
		return true
	}
	r.Body.Close()
	r.Body = io.NopCloser(bytes.NewReader(data))
	var event map[string]any
	if json.Unmarshal(data, &event) != nil {
		core.ServeHTTP(w, r)
		return true
	}
	event["Client"] = r.UserAgent()
	if strings.HasSuffix(path, "/stopped") {
		event["Event"] = "stopped"
	} else if path == "/sessions/playing" {
		event["Event"] = "playing"
	}
	tracked := &featureEventResponse{ResponseWriter: w}
	core.ServeHTTP(tracked, r)
	if tracked.status >= 200 && tracked.status < 300 {
		payload, _ := json.Marshal(event)
		req, _ := http.NewRequestWithContext(r.Context(), "POST", scraperURL+"/features/playback-event", bytes.NewReader(payload))
		req.Header = r.Header.Clone()
		req.Header.Set("X-Emby-Token", featureGatewayToken(r))
		req.Header.Set("Content-Type", "application/json")
		if response, e := (&http.Client{Timeout: 3 * time.Second}).Do(req); e == nil {
			response.Body.Close()
		}
	}
	return true
}
