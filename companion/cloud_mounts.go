package main

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
)

func cloudInt(n int64) string { return strconv.FormatInt(n, 10) }

type cloudMount struct {
	ID           string
	Name         string
	Driver       string
	StorageID    int64  `json:"-"`
	Secret       string `json:"-"`
	Enabled      bool
	Status       string
	PlaybackMode string
}

func (a *App) cloudMount(id string) (cloudMount, error) {
	var m cloudMount
	var enabled int
	err := a.db.QueryRow("SELECT id,name,driver,storage_id,secret,enabled,playback_mode FROM feature_cloud_mounts WHERE id=?", id).Scan(&m.ID, &m.Name, &m.Driver, &m.StorageID, &m.Secret, &enabled, &m.PlaybackMode)
	if m.Driver == "WebDav" {
		m.PlaybackMode = "proxy"
	}
	m.Enabled = enabled == 1
	return m, err
}
func cloudPath(raw string) (string, error) {
	if raw == "" {
		return "/", nil
	}
	if len(raw) > 8192 || strings.ContainsAny(raw, "\\\x00\r\n") {
		return "", errors.New("网盘目录不合法")
	}
	for _, segment := range strings.Split(raw, "/") {
		if segment == ".." || segment == "." {
			return "", errors.New("禁止目录遍历")
		}
	}
	return path.Clean("/" + strings.TrimPrefix(raw, "/")), nil
}
func cloudStoragePath(m cloudMount, p string) string {
	return "/cloud/" + m.ID + strings.TrimSuffix(p, "/")
}

func (a *App) cloudAdmin(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/admin/features/cloud/tasks":
		if featureMethod(w, r, "GET") {
			respond(w, M{"Tasks": a.cloudTasks()})
		}
	case "/admin/features/cloud":
		if !featureMethod(w, r, "GET") {
			return
		}
		rows, err := a.db.Query("SELECT id FROM feature_cloud_mounts ORDER BY created,id")
		if err != nil {
			featureError(w, err)
			return
		}
		ids := []string{}
		for rows.Next() {
			var id string
			if rows.Scan(&id) == nil {
				ids = append(ids, id)
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			featureError(w, err)
			return
		}
		var storages struct {
			Content []cloudStorage `json:"content"`
		}
		engineErr := cloudCall(r.Context(), "GET", "/api/admin/storage/list?page=1&per_page=1000", nil, &storages, "")
		mounts := []cloudMount{}
		for _, id := range ids {
			m, e := a.cloudMount(id)
			if e != nil {
				continue
			}
			m.Status = "账号待检查"
			if engineErr != nil {
				m.Status = "引擎连接中"
			} else {
				for _, s := range storages.Content {
					if s.ID == m.StorageID {
						if s.Status == "work" {
							m.Status = "已连接"
						}
						if s.Disabled {
							m.Status = "已暂停"
						}
						break
					}
				}
			}
			if !m.Enabled {
				m.Status = "已暂停"
			}
			mounts = append(mounts, m)
		}
		var network featureNetworkSettings
		a.featureSetting("network", &network)
		respond(w, M{"Mounts": mounts, "Drivers": cloudDrivers, "PublicURL": network.PublicURL, "EngineReady": engineErr == nil, "Tasks": a.cloudTasks()})
	case "/admin/features/cloud/fields":
		if !featureMethod(w, r, "GET") {
			return
		}
		driver := r.URL.Query().Get("Driver")
		if cloudDrivers[driver] == "" {
			fail(w, 400, "不支持的网盘")
			return
		}
		var schema cloudDriver
		if err := cloudCall(r.Context(), "GET", "/api/admin/driver/info?driver="+url.QueryEscape(driver), nil, &schema, ""); err != nil {
			fail(w, 502, err.Error())
			return
		}
		values := map[string]any{}
		saved := []string{}
		if mountID := r.URL.Query().Get("ID"); mountID != "" {
			m, err := a.cloudMount(mountID)
			if err != nil {
				featureError(w, err)
				return
			}
			if m.Driver != driver {
				fail(w, 400, "网盘类型不一致")
				return
			}
			s, err := cloudGetStorage(r.Context(), m.StorageID)
			if err != nil {
				fail(w, 502, err.Error())
				return
			}
			var addition map[string]any
			_ = json.Unmarshal([]byte(s.Addition), &addition)
			for k, v := range addition {
				if cloudSensitive(k) {
					if v != nil && v != "" {
						saved = append(saved, k)
					}
				} else {
					values[k] = v
				}
			}
		}
		respond(w, M{"Fields": schema.Additional, "Values": values, "Saved": saved})
	case "/admin/features/cloud/save":
		a.cloudSave(w, r)
	case "/admin/features/cloud/action":
		a.cloudAction(w, r)
	case "/admin/features/cloud/list":
		if !featureMethod(w, r, "GET") {
			return
		}
		m, err := a.cloudMount(r.URL.Query().Get("ID"))
		if err != nil {
			featureError(w, err)
			return
		}
		if !m.Enabled {
			fail(w, 409, "挂载已暂停")
			return
		}
		p, err := cloudPath(r.URL.Query().Get("Path"))
		if err != nil {
			fail(w, 400, err.Error())
			return
		}
		page, _ := strconv.Atoi(r.URL.Query().Get("Page"))
		if page < 1 {
			page = 1
		}
		if page > 2000 {
			fail(w, 400, "目录页数过大")
			return
		}
		listing, err := cloudList(r.Context(), m, p, page, strings.EqualFold(r.URL.Query().Get("Refresh"), "true"))
		if err != nil {
			fail(w, 502, err.Error())
			return
		}
		respond(w, M{"Path": p, "Items": listing.Content, "Total": listing.Total, "Page": page, "PerPage": 200})
	case "/admin/features/cloud/generate":
		a.cloudGenerateAPI(w, r)
	case "/admin/features/cloud/cancel":
		if !featureMethod(w, r, "POST") {
			return
		}
		var b struct{ ID string }
		if !body(w, r, &b) {
			return
		}
		a.features.mu.Lock()
		cancel := a.features.jobs["cloud:"+b.ID]
		if cancel != nil {
			cancel()
		}
		a.features.mu.Unlock()
		respond(w, M{"Cancelled": cancel != nil})
	default:
		http.NotFound(w, r)
	}
}

func (a *App) cloudSave(w http.ResponseWriter, r *http.Request) {
	if !featureMethod(w, r, "POST") {
		return
	}
	var b struct {
		ID, Name, Driver string
		PlaybackMode     string
		Addition         map[string]any
	}
	if !body(w, r, &b) {
		return
	}
	b.Name = strings.TrimSpace(b.Name)
	if b.Name == "" || len(b.Name) > 256 || cloudDrivers[b.Driver] == "" {
		fail(w, 400, "请填写名称并选择支持的网盘")
		return
	}
	if b.PlaybackMode != "" && b.PlaybackMode != "redirect" && b.PlaybackMode != "proxy" {
		fail(w, 400, "请选择直链 302 或服务器中转")
		return
	}
	cloudManageMu.Lock()
	defer cloudManageMu.Unlock()
	var schema cloudDriver
	if err := cloudCall(r.Context(), "GET", "/api/admin/driver/info?driver="+url.QueryEscape(b.Driver), nil, &schema, ""); err != nil {
		fail(w, 502, err.Error())
		return
	}
	m := cloudMount{ID: id(), Name: b.Name, Driver: b.Driver, Enabled: true}
	fresh := b.ID == ""
	s := cloudStorage{Driver: b.Driver, CacheExpiration: 15, WebdavPolicy: "302_redirect", EnableSign: true}
	addition := map[string]any{}
	if !fresh {
		var err error
		m, err = a.cloudMount(b.ID)
		if err != nil {
			featureError(w, err)
			return
		}
		if m.Driver != b.Driver {
			fail(w, 400, "编辑时不能更换网盘类型")
			return
		}
		if a.cloudJobRunning(m.ID) {
			fail(w, 409, "请先取消该挂载的生成任务")
			return
		}
		s, err = cloudGetStorage(r.Context(), m.StorageID)
		if err != nil {
			fail(w, 502, err.Error())
			return
		}
		_ = json.Unmarshal([]byte(s.Addition), &addition)
	}
	if b.PlaybackMode != "" {
		m.PlaybackMode = b.PlaybackMode
	}
	if m.PlaybackMode == "" {
		m.PlaybackMode = "redirect"
	}
	if b.Driver == "WebDav" {
		m.PlaybackMode = "proxy"
	}
	for _, f := range schema.Additional {
		v, ok := b.Addition[f.Name]
		if !ok || (cloudSensitive(f.Name) && v == "") {
			if _, exists := addition[f.Name]; !exists && f.Default != "" {
				switch f.Type {
				case "bool":
					addition[f.Name] = f.Default == "true"
				case "number", "float":
					n, _ := strconv.ParseFloat(f.Default, 64)
					addition[f.Name] = n
				default:
					addition[f.Name] = f.Default
				}
			}
			continue
		}
		if v != nil {
			switch f.Type {
			case "bool":
				if _, ok := v.(bool); !ok {
					fail(w, 400, "开关字段格式错误")
					return
				}
			case "number", "float":
				if _, ok := v.(float64); !ok {
					fail(w, 400, "数字字段格式错误")
					return
				}
			default:
				if _, ok := v.(string); !ok {
					fail(w, 400, "文字字段格式错误")
					return
				}
			}
		}
		addition[f.Name] = v
	}
	for _, f := range schema.Additional {
		if f.Required && (addition[f.Name] == nil || addition[f.Name] == "") {
			fail(w, 400, "请填写必要的账号配置")
			return
		}
	}
	if b.Driver == "WebDav" {
		if err := cloudWebDAVAddition(addition); err != nil {
			fail(w, 400, err.Error())
			return
		}
	}
	if b.Driver == "Quark" && m.PlaybackMode == "proxy" {
		addition["use_transcoding_address"] = false
	}
	s.MountPath = cloudStoragePath(m, "/")
	s.Addition = featureJSON(addition)
	s.WebProxy = false
	s.EnableSign = true
	s.WebdavPolicy = "302_redirect"
	if m.PlaybackMode == "proxy" {
		s.WebProxy = true
		s.WebdavPolicy = "native_proxy"
	}
	if fresh {
		var secret [32]byte
		if _, err := rand.Read(secret[:]); err != nil {
			featureError(w, err)
			return
		}
		m.Secret = hex.EncodeToString(secret[:])
		_, err := a.db.Exec("INSERT INTO feature_cloud_mounts(id,name,driver,secret,enabled,created,playback_mode) VALUES(?,?,?,?,1,?,?)", m.ID, b.Name, b.Driver, m.Secret, featureNow(), m.PlaybackMode)
		if err != nil {
			featureError(w, err)
			return
		}
		var created struct {
			ID int64 `json:"id"`
		}
		engineErr := cloudCall(r.Context(), "POST", "/api/admin/storage/create", s, &created, "")
		if created.ID == 0 {
			// A cancelled response can still leave an engine storage behind.
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			var list struct {
				Content []cloudStorage `json:"content"`
			}
			_ = cloudCall(ctx, "GET", "/api/admin/storage/list?page=1&per_page=1000", nil, &list, "")
			for _, storage := range list.Content {
				if storage.MountPath == s.MountPath {
					created.ID = storage.ID
					break
				}
			}
		}
		if created.ID == 0 {
			a.db.Exec("DELETE FROM feature_cloud_mounts WHERE id=?", m.ID)
			if engineErr == nil {
				engineErr = errCloudAccount
			}
			fail(w, 502, engineErr.Error())
			return
		}
		if _, err = a.db.Exec("UPDATE feature_cloud_mounts SET storage_id=? WHERE id=?", created.ID, m.ID); err != nil {
			featureError(w, err)
			return
		}
		respond(w, M{"ID": m.ID, "Connected": engineErr == nil, "Message": cloudSaveMessage(engineErr)})
		return
	}
	err := cloudCall(r.Context(), "POST", "/api/admin/storage/update", s, nil, "")
	if _, e := a.db.Exec("UPDATE feature_cloud_mounts SET name=?,playback_mode=? WHERE id=?", b.Name, m.PlaybackMode, m.ID); e != nil {
		featureError(w, e)
		return
	}
	respond(w, M{"ID": m.ID, "Connected": err == nil, "Message": cloudSaveMessage(err)})
}

func cloudSaveMessage(err error) string {
	if err != nil {
		return "配置已保存，账号尚未连接，请检查配置或完成短信登录"
	}
	return "挂载已保存"
}
func (a *App) cloudJobRunning(id string) bool {
	a.features.mu.Lock()
	defer a.features.mu.Unlock()
	return a.features.jobs["cloud:"+id] != nil
}

func (a *App) cloudAction(w http.ResponseWriter, r *http.Request) {
	if !featureMethod(w, r, "POST") {
		return
	}
	var b struct{ ID, Action string }
	if !body(w, r, &b) {
		return
	}
	if b.Action != "delete" && b.Action != "enable" && b.Action != "disable" {
		fail(w, 400, "不支持此操作")
		return
	}
	cloudManageMu.Lock()
	defer cloudManageMu.Unlock()
	m, err := a.cloudMount(b.ID)
	if err != nil {
		featureError(w, err)
		return
	}
	if a.cloudJobRunning(m.ID) {
		fail(w, 409, "请先取消生成任务")
		return
	}
	if b.Action == "disable" || b.Action == "delete" {
		if _, err = a.db.Exec("UPDATE feature_cloud_mounts SET enabled=0 WHERE id=?", m.ID); err != nil {
			featureError(w, err)
			return
		}
	}
	if err = cloudCall(r.Context(), "POST", "/api/admin/storage/"+b.Action+"?id="+cloudInt(m.StorageID), nil, nil, ""); err != nil {
		fail(w, 502, err.Error())
		return
	}
	if b.Action == "delete" {
		_, err = a.db.Exec("DELETE FROM feature_cloud_mounts WHERE id=?", m.ID)
	} else {
		enabled := 0
		if b.Action == "enable" {
			enabled = 1
		}
		_, err = a.db.Exec("UPDATE feature_cloud_mounts SET enabled=? WHERE id=?", enabled, m.ID)
	}
	if err != nil {
		featureError(w, err)
		return
	}
	respond(w, M{"ok": true})
}

type cloudObject struct {
	Name  string `json:"name"`
	IsDir bool   `json:"is_dir"`
	Size  int64  `json:"size"`
}
type cloudListing struct {
	Content []cloudObject `json:"content"`
	Total   int           `json:"total"`
}

func cloudList(ctx context.Context, m cloudMount, p string, page int, refresh bool) (cloudListing, error) {
	var list cloudListing
	err := cloudCall(ctx, "POST", "/api/fs/list", M{"path": cloudStoragePath(m, p), "page": page, "per_page": 200, "refresh": refresh}, &list, "")
	if list.Content == nil {
		list.Content = []cloudObject{}
	}
	return list, err
}
func cloudSign(m cloudMount, p string) string {
	h := hmac.New(sha256.New, []byte(m.Secret))
	h.Write([]byte(m.ID + "\x00" + p))
	return hex.EncodeToString(h.Sum(nil))
}

func (a *App) cloudResolve(w http.ResponseWriter, r *http.Request) {
	if !featureMethod(w, r, "GET", "HEAD") {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if len(r.URL.RawQuery) > 20000 {
		fail(w, 400, "播放地址不合法")
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/cloud/resolve/")
	m, err := a.cloudMount(id)
	if err != nil || !m.Enabled {
		fail(w, 403, "播放链接已停用")
		return
	}
	p, err := cloudPath(r.URL.Query().Get("path"))
	if err != nil || p == "/" {
		fail(w, 403, "播放链接无效")
		return
	}
	provided, err := hex.DecodeString(r.URL.Query().Get("sign"))
	expected, _ := hex.DecodeString(cloudSign(m, p))
	if err != nil || !hmac.Equal(provided, expected) {
		fail(w, 403, "播放链接签名无效")
		return
	}
	if m.PlaybackMode == "proxy" {
		a.cloudProxyStream(w, r, m, p)
		return
	}
	var link struct {
		URL    string      `json:"url"`
		Header http.Header `json:"header"`
	}
	if err = cloudCall(r.Context(), "POST", "/api/fs/link", M{"path": cloudStoragePath(m, p)}, &link, r.UserAgent()); err != nil {
		fail(w, 502, err.Error())
		return
	}
	u, err := url.Parse(link.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost" || strings.ContainsAny(link.URL, "\r\n") {
		fail(w, 502, "网盘未提供可用直链")
		return
	}
	for key, values := range link.Header {
		if len(values) == 0 {
			continue
		}
		switch strings.ToLower(key) {
		case "user-agent":
			if strings.Join(values, "") != r.UserAgent() {
				fail(w, 409, "网盘直链绑定了不同的客户端，请使用当前客户端重新播放")
				return
			}
		case "accept", "accept-encoding", "range":
		default:
			fail(w, 409, fmt.Sprintf("%s 当前链接需要额外请求头，请选择网盘支持的直链模式", cloudDrivers[m.Driver]))
			return
		}
	}
	http.Redirect(w, r, link.URL, http.StatusFound)
}
