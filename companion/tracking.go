package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

type trackingSettings struct {
	URL, Token, Username, Password string
	Enabled                        bool
	AutoScrape, AutoRename         *bool `json:",omitempty"`
}
type trackingSubscription struct {
	ID, Title, Query, ItemID, Source string
	Year, Minutes                    int
	CloudTypes, Include, Exclude     []string
	Enabled                          bool
	AutoImport                       trackingImportConfig
}
type trackingRecord struct {
	trackingSubscription
	Created, LastSearch, NextSearch    int64
	State, Error                       string
	LastCount, NewCount, ResourceCount int
	Owned                              string
	ImportCloud, ImportMount           string
	Import                             trackingImportState
}
type trackingResource struct {
	ID, Subscription, Cloud, Title, URL, Password, Source, Published, Status string
	FirstSeen, Updated                                                       int64
}

var trackingClouds = map[string]string{"mobile": "移动云盘", "115": "115", "quark": "夸克", "guangya": "光鸭", "aliyun": "阿里云盘", "baidu": "百度网盘", "tianyi": "天翼云盘", "uc": "UC", "123": "123 云盘", "pikpak": "PikPak", "xunlei": "迅雷"}

func (a *App) trackingConfig() trackingSettings {
	var c trackingSettings
	a.featureSetting("tracking", &c)
	return c
}
func trackingPublicConfig(c trackingSettings) M {
	return M{"URL": c.URL, "Enabled": c.Enabled, "Username": c.Username, "HasToken": c.Token != "", "HasPassword": c.Password != "", "AutoScrape": trackingOption(c.AutoScrape, true), "AutoRename": trackingOption(c.AutoRename, false)}
}

func trackingOption(value *bool, fallback bool) bool {
	if value == nil {
		return fallback
	}
	return *value
}
func trackingBase(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("请填写 PanSou 服务的 HTTP 或 HTTPS 地址，不要包含账号、查询参数或页面锚点")
	}
	u.Path = strings.TrimSuffix(strings.TrimRight(u.Path, "/"), "/api/search")
	u.Path = strings.TrimSuffix(u.Path, "/api")
	u.RawPath = ""
	return strings.TrimRight(u.String(), "/"), nil
}
func trackingWords(values []string) ([]string, error) {
	if len(values) > 20 {
		return nil, errors.New("每组筛选词最多 20 个")
	}
	out := []string{}
	seen := map[string]bool{}
	for _, s := range values {
		s = strings.TrimSpace(s)
		if len(s) > 256 {
			return nil, errors.New("筛选词过长")
		}
		if s != "" && !seen[s] {
			out = append(out, s)
			seen[s] = true
		}
	}
	return out, nil
}
func (a *App) trackingAPI(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/admin/features/tracking":
		if !featureMethod(w, r, "GET") {
			return
		}
		items, err := a.trackingSubscriptions()
		if err != nil {
			featureError(w, err)
			return
		}
		a.features.trackingMu.Lock()
		busy := a.features.trackingBusy
		a.features.trackingMu.Unlock()
		respond(w, M{"Settings": trackingPublicConfig(a.trackingConfig()), "Subscriptions": items, "CloudTypes": trackingClouds, "Busy": busy})
	case "/admin/features/tracking/settings":
		if !featureMethod(w, r, "PUT") {
			return
		}
		var b struct {
			trackingSettings
			ClearCredentials bool
		}
		if !body(w, r, &b) {
			return
		}
		base, err := trackingBase(b.URL)
		if err != nil {
			fail(w, 400, err.Error())
			return
		}
		old := a.trackingConfig()
		if b.AutoScrape == nil {
			b.AutoScrape = old.AutoScrape
		}
		if b.AutoRename == nil {
			b.AutoRename = old.AutoRename
		}
		if b.ClearCredentials {
			old.Token = ""
			old.Password = ""
		}
		if base != old.URL && !b.ClearCredentials {
			old.Token = ""
			old.Password = ""
		}
		if b.Token == "" {
			b.Token = old.Token
		}
		if b.Password == "" && b.Username == old.Username {
			b.Password = old.Password
		}
		b.Token = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(b.Token), "Bearer "))
		if len(b.Token) > 16384 || len(b.Password) > 4096 || len(b.Username) > 256 || strings.ContainsAny(b.Token, "\r\n") {
			fail(w, 400, "认证信息格式无效")
			return
		}
		b.URL = base
		if err = a.saveFeatureSetting("tracking", b.trackingSettings); err != nil {
			featureError(w, err)
			return
		}
		respond(w, trackingPublicConfig(b.trackingSettings))
	case "/admin/features/tracking/connect":
		if !featureMethod(w, r, "POST") {
			return
		}
		c := a.trackingConfig()
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		var health struct {
			Status   string `json:"status"`
			Auth     bool   `json:"auth_enabled"`
			Plugins  int    `json:"plugin_count"`
			Channels int    `json:"channels_count"`
		}
		if err := trackingRequest(ctx, c, "GET", "/api/health", nil, "", &health); err != nil {
			fail(w, 502, err.Error())
			return
		}
		if health.Status != "ok" {
			fail(w, 502, "此地址没有返回 PanSou 健康状态，请填写 API 服务地址")
			return
		}
		if health.Auth {
			token, err := trackingAuth(ctx, c)
			if err != nil {
				fail(w, 502, err.Error())
				return
			}
			if token == "" {
				fail(w, 400, "PanSou 已启用认证，请填写账号密码或 Token")
				return
			}
			var verified struct {
				Valid bool `json:"valid"`
			}
			if err = trackingRequest(ctx, c, "POST", "/api/auth/verify", M{}, token, &verified); err != nil {
				fail(w, 502, err.Error())
				return
			}
			if !verified.Valid {
				fail(w, 502, "PanSou 认证已失效，请重新配置")
				return
			}
		}
		respond(w, M{"Connected": true, "Plugins": health.Plugins, "Channels": health.Channels, "Auth": health.Auth})
	case "/admin/features/tracking/subscription":
		if !featureMethod(w, r, "POST", "DELETE") {
			return
		}
		a.features.trackingMu.Lock()
		defer a.features.trackingMu.Unlock()
		if a.features.trackingBusy {
			fail(w, 409, "追新搜索正在执行，请等待结束后编辑订阅")
			return
		}
		if r.Method == "DELETE" {
			result, err := a.db.Exec("DELETE FROM feature_tracking_subscriptions WHERE id=?", r.URL.Query().Get("ID"))
			if err != nil {
				featureError(w, err)
				return
			}
			n, _ := result.RowsAffected()
			if n == 0 {
				fail(w, 404, "订阅不存在")
				return
			}
			respond(w, M{"ok": true})
			return
		}
		var b trackingSubscription
		if !body(w, r, &b) {
			return
		}
		b.Title = strings.TrimSpace(b.Title)
		b.Query = strings.TrimSpace(b.Query)
		if b.Query == "" {
			b.Query = b.Title
			if b.Year > 0 {
				b.Query += " " + strconv.Itoa(b.Year)
			}
		}
		if b.Title == "" || len(b.Title) > 512 || len(b.Query) > 1024 || b.Year < 0 || b.Year > 9999 || b.Minutes < 15 || b.Minutes > 10080 {
			fail(w, 400, "填写作品名称；搜索间隔为 15–10080 分钟")
			return
		}
		if b.Source == "" {
			b.Source = "all"
		}
		if b.Source != "all" && b.Source != "tg" && b.Source != "plugin" {
			fail(w, 400, "搜索来源无效")
			return
		}
		if len(b.CloudTypes) > len(trackingClouds) {
			fail(w, 400, "网盘类型无效")
			return
		}
		seen := map[string]bool{}
		clouds := []string{}
		for _, c := range b.CloudTypes {
			if trackingClouds[c] == "" {
				fail(w, 400, "网盘类型无效")
				return
			}
			if !seen[c] {
				clouds = append(clouds, c)
				seen[c] = true
			}
		}
		b.CloudTypes = clouds
		var err error
		b.Include, err = trackingWords(b.Include)
		if err == nil {
			b.Exclude, err = trackingWords(b.Exclude)
		}
		if err != nil {
			fail(w, 400, err.Error())
			return
		}
		if b.ItemID != "" {
			item, e := a.item(b.ItemID)
			if e != nil || (item.Kind != "Series" && item.Kind != "Movie") {
				fail(w, 400, "请关联媒体库中的电影或整部剧集")
				return
			}
		}
		if err := a.trackingValidateImport(&b); err != nil {
			fail(w, 400, err.Error())
			return
		}
		resetImport := b.AutoImport.Reselect
		b.AutoImport.Reselect = false
		if b.ID == "" {
			var count int
			if err = a.db.QueryRow("SELECT COUNT(*) FROM feature_tracking_subscriptions").Scan(&count); err != nil {
				featureError(w, err)
				return
			}
			if count >= 100 {
				fail(w, 400, "最多创建 100 个订阅")
				return
			}
			b.ID = id()
			_, err = a.db.Exec("INSERT INTO feature_tracking_subscriptions(id,data,created,next_search) VALUES(?,?,?,?)", b.ID, featureJSON(b), time.Now().Unix(), time.Now().Unix())
		} else {
			var result sql.Result
			result, err = a.db.Exec("UPDATE feature_tracking_subscriptions SET data=?,next_search=? WHERE id=?", featureJSON(b), time.Now().Unix(), b.ID)
			if err == nil {
				n, _ := result.RowsAffected()
				if n == 0 {
					fail(w, 404, "订阅不存在")
					return
				}
			}
		}
		if err != nil {
			featureError(w, err)
			return
		}
		if resetImport {
			if _, err = a.db.Exec("DELETE FROM feature_tracking_imports WHERE subscription=?", b.ID); err != nil {
				featureError(w, err)
				return
			}
		}
		respond(w, b)
	case "/admin/features/tracking/run":
		if !featureMethod(w, r, "POST") {
			return
		}
		var b struct{ ID string }
		if !body(w, r, &b) {
			return
		}
		if a.trackingConfig().URL == "" {
			fail(w, 400, "请先配置 PanSou 服务地址")
			return
		}
		items, err := a.trackingSubscriptions()
		if err != nil {
			featureError(w, err)
			return
		}
		ids := []string{}
		for _, s := range items {
			if (b.ID == "" && s.Enabled) || s.ID == b.ID {
				ids = append(ids, s.ID)
			}
		}
		if len(ids) == 0 {
			fail(w, 400, "没有可以搜索的订阅")
			return
		}
		if !a.trackingQueue(ids) {
			fail(w, 409, "追新搜索正在执行，请稍后刷新结果")
			return
		}
		respond(w, M{"Queued": len(ids)})
	case "/admin/features/tracking/import/options", "/admin/features/tracking/import/resource", "/admin/features/tracking/import/start":
		a.trackingImportAPI(w, r)
	case "/admin/features/tracking/resources":
		a.trackingResourcesAPI(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (a *App) trackingSubscriptions() ([]trackingRecord, error) {
	rows, err := a.db.Query("SELECT id,data,created,last_search,next_search,state,error,last_count FROM feature_tracking_subscriptions ORDER BY created DESC,id")
	if err != nil {
		return nil, err
	}
	items := []trackingRecord{}
	for rows.Next() {
		var s trackingRecord
		var raw string
		if err = rows.Scan(&s.ID, &raw, &s.Created, &s.LastSearch, &s.NextSearch, &s.State, &s.Error, &s.LastCount); err != nil {
			break
		}
		if err = json.Unmarshal([]byte(raw), &s.trackingSubscription); err != nil {
			break
		}
		items = append(items, s)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	for i := range items {
		s := &items[i]
		if err = a.db.QueryRow("SELECT COUNT(*),COUNT(*) FILTER (WHERE status='new') FROM feature_tracking_resources WHERE subscription=?", s.ID).Scan(&s.ResourceCount, &s.NewCount); err != nil {
			return nil, err
		}
		s.Owned = a.trackingOwned(s.ItemID)
		if s.AutoImport.Enabled {
			if m, e := a.cloudMount(s.AutoImport.MountID); e == nil {
				s.ImportCloud, s.ImportMount = trackingMountCloud(m.Driver), m.Name
			}
		}
		s.Import, err = a.trackingImportState(s.ID)
		if err != nil {
			return nil, err
		}
		if s.Import.ManualConfig != nil {
			if m, e := a.cloudMount(s.Import.ManualConfig.MountID); e == nil {
				s.ImportCloud, s.ImportMount = trackingMountCloud(m.Driver), m.Name
			}
		}
		s.Import.PendingFiles, s.Import.PendingParent, s.Import.ConfigKey = nil, "", ""
		if s.Import.PendingTask != "" {
			s.Import.PendingTask = "pending"
		}
	}
	return items, nil
}
func (a *App) trackingOwned(itemID string) string {
	if itemID == "" {
		return ""
	}
	item, err := a.item(itemID)
	if err != nil {
		return "关联作品已不在媒体库"
	}
	if item.Kind == "Movie" {
		return "电影已入库"
	}
	rows, err := a.db.Query(`WITH RECURSIVE descendants AS (SELECT id,kind,season,episode FROM items WHERE parent=? UNION ALL SELECT i.id,i.kind,i.season,i.episode FROM items i JOIN descendants d ON i.parent=d.id) SELECT season,COUNT(DISTINCT episode),MAX(episode) FROM descendants WHERE kind='Episode' GROUP BY season ORDER BY season`, itemID)
	if err != nil {
		return "暂时无法读取已有集数"
	}
	defer rows.Close()
	parts := []string{}
	for rows.Next() {
		var season, count, last int
		if rows.Scan(&season, &count, &last) == nil {
			parts = append(parts, fmt.Sprintf("S%02d · %d 集 / 至 E%02d", season, count, last))
		}
	}
	if rows.Err() != nil {
		return "暂时无法读取已有集数"
	}
	if len(parts) == 0 {
		return "媒体库暂无单集"
	}
	return strings.Join(parts, "；")
}
func (a *App) trackingResourcesAPI(w http.ResponseWriter, r *http.Request) {
	if !featureMethod(w, r, "GET", "PUT") {
		return
	}
	if r.Method == "PUT" {
		var b struct {
			IDs          []string
			Status       string
			Subscription string
		}
		if !body(w, r, &b) {
			return
		}
		if b.Status != "seen" && b.Status != "ignored" && b.Status != "new" {
			fail(w, 400, "资源状态无效")
			return
		}
		if len(b.IDs) > 100 || (len(b.IDs) == 0 && b.Subscription == "") {
			fail(w, 400, "请选择资源")
			return
		}
		tx, err := a.db.DB.BeginTx(r.Context(), nil)
		if err != nil {
			featureError(w, err)
			return
		}
		defer tx.Rollback()
		if len(b.IDs) == 0 {
			_, err = tx.ExecContext(r.Context(), "UPDATE feature_tracking_resources SET status=$1 WHERE subscription=$2 AND status='new'", b.Status, b.Subscription)
		} else {
			for _, rid := range b.IDs {
				if len(rid) > 128 {
					fail(w, 400, "资源编号无效")
					return
				}
				if _, err = tx.ExecContext(r.Context(), "UPDATE feature_tracking_resources SET status=$1 WHERE id=$2", b.Status, rid); err != nil {
					break
				}
			}
		}
		if err == nil {
			err = tx.Commit()
		}
		if err != nil {
			featureError(w, err)
			return
		}
		respond(w, M{"ok": true})
		return
	}
	query := " FROM feature_tracking_resources WHERE 1=1"
	args := []any{}
	for _, filter := range []struct{ param, column string }{{"Subscription", "subscription"}, {"Status", "status"}, {"Cloud", "cloud"}} {
		if v := r.URL.Query().Get(filter.param); v != "" {
			query += " AND " + filter.column + "=?"
			args = append(args, v)
		}
	}
	var total int
	if err := a.db.QueryRow("SELECT COUNT(*)"+query, args...).Scan(&total); err != nil {
		featureError(w, err)
		return
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("Page"))
	page = max(1, min(page, 100000))
	limit := featureLimit(r, 30, 100)
	rows, err := a.db.Query("SELECT id,subscription,cloud,data,status,first_seen,updated"+query+" ORDER BY updated DESC,id LIMIT ? OFFSET ?", append(args, limit, (page-1)*limit)...)
	if err != nil {
		featureError(w, err)
		return
	}
	defer rows.Close()
	items := []trackingResource{}
	for rows.Next() {
		var s trackingResource
		var raw string
		if err = rows.Scan(&s.ID, &s.Subscription, &s.Cloud, &raw, &s.Status, &s.FirstSeen, &s.Updated); err != nil {
			break
		}
		var data trackingResource
		if err = json.Unmarshal([]byte(raw), &data); err != nil {
			break
		}
		s.Title = data.Title
		s.URL = data.URL
		s.Password = data.Password
		s.Source = data.Source
		s.Published = data.Published
		items = append(items, s)
	}
	if err == nil {
		err = rows.Err()
	}
	if err != nil {
		featureError(w, err)
		return
	}
	respond(w, M{"Items": items, "Total": total, "Page": page, "Limit": limit})
}

func trackingRequest(ctx context.Context, c trackingSettings, method, endpoint string, payload any, token string, target any) error {
	base, err := trackingBase(c.URL)
	if err != nil {
		return errors.New("请先配置有效的 PanSou 服务地址")
	}
	var reader io.Reader
	if payload != nil {
		data, e := json.Marshal(payload)
		if e != nil {
			return e
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, base+endpoint, reader)
	if err != nil {
		return errors.New("PanSou 请求配置无效")
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := http.Client{Timeout: 90 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return errors.New("PanSou 请求已取消或超时")
		}
		return errors.New("无法连接 PanSou，请检查服务地址与网络")
	}
	defer res.Body.Close()
	if res.StatusCode == 401 || res.StatusCode == 403 {
		return errors.New("PanSou 认证失败，请检查账号密码或更新 Token")
	}
	if res.StatusCode != 200 {
		return fmt.Errorf("PanSou 返回 HTTP %d，请检查 API 地址和服务状态", res.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, (8<<20)+1))
	if err != nil || len(data) > 8<<20 {
		return errors.New("PanSou 返回内容过大或读取失败")
	}
	var envelope struct {
		Code  int             `json:"code"`
		Data  json.RawMessage `json:"data"`
		Error string          `json:"error"`
	}
	if json.Unmarshal(data, &envelope) != nil {
		return errors.New("PanSou 返回格式无效，请填写 API 服务地址")
	}
	if envelope.Code != 0 || envelope.Error != "" {
		return errors.New("PanSou 拒绝了请求，请检查配置和认证")
	}
	if len(envelope.Data) > 0 && string(envelope.Data) != "null" {
		data = envelope.Data
	}
	if err = json.Unmarshal(data, target); err != nil {
		return errors.New("PanSou 返回格式无法识别")
	}
	return nil
}
func trackingAuth(ctx context.Context, c trackingSettings) (string, error) {
	if c.Username != "" && c.Password != "" {
		var login struct {
			Token string `json:"token"`
		}
		if err := trackingRequest(ctx, c, "POST", "/api/auth/login", M{"username": c.Username, "password": c.Password}, "", &login); err != nil {
			return "", err
		}
		if login.Token == "" {
			return "", errors.New("PanSou 登录未返回 Token")
		}
		return login.Token, nil
	}
	return c.Token, nil
}
func trackingTrim(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) > n {
		r = r[:n]
	}
	return string(r)
}
func trackingResourceURL(raw string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || len(raw) > 8192 {
		return "", false
	}
	u.Host = strings.ToLower(u.Host)
	// Some providers put the share identifier in the URL fragment.
	u.RawQuery = u.Query().Encode()
	u.Path = strings.TrimRight(u.Path, "/")
	return u.String(), true
}
func (a *App) trackingSearch(ctx context.Context, c trackingSettings, s trackingSubscription) (int, int, error) {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	token, err := trackingAuth(ctx, c)
	if err != nil {
		return 0, 0, err
	}
	var result struct {
		Total  *int                                                                `json:"total"`
		Merged map[string][]struct{ URL, Password, Note, Datetime, Source string } `json:"merged_by_type"`
	}
	request := M{"kw": s.Query, "res": "merge", "src": s.Source, "cloud_types": s.CloudTypes, "filter": M{"include": s.Include, "exclude": s.Exclude}, "conc": 8, "refresh": true}
	if err = trackingRequest(ctx, c, "POST", "/api/search", request, token, &result); err != nil {
		return 0, 0, err
	}
	if result.Total == nil && result.Merged == nil {
		return 0, 0, errors.New("此地址未返回 PanSou 搜索结果")
	}
	if result.Total != nil && *result.Total == 0 && s.Source != "tg" {
		timer := time.NewTimer(8 * time.Second)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return 0, 0, ctx.Err()
		case <-timer.C:
		}
		request["refresh"] = false
		if err = trackingRequest(ctx, c, "POST", "/api/search", request, token, &result); err != nil {
			return 0, 0, err
		}
		if result.Total == nil && result.Merged == nil {
			return 0, 0, errors.New("此地址未返回 PanSou 搜索结果")
		}
	}
	resources := []trackingResource{}
	seen := map[string]bool{}
	clouds := []string{}
	for cloud := range result.Merged {
		clouds = append(clouds, cloud)
	}
	sort.Strings(clouds)
	for _, cloud := range clouds {
		if trackingClouds[cloud] == "" {
			continue
		}
		if len(s.CloudTypes) > 0 && !trackingContains(s.CloudTypes, cloud) {
			continue
		}
		for _, link := range result.Merged[cloud] {
			u, ok := trackingResourceURL(link.URL)
			if !ok || seen[u] {
				continue
			}
			seen[u] = true
			resources = append(resources, trackingResource{ID: digest(s.ID + "\x00" + u), Subscription: s.ID, Cloud: cloud, Title: trackingTrim(link.Note, 2048), URL: u, Password: trackingTrim(link.Password, 256), Source: trackingTrim(link.Source, 256), Published: trackingTrim(link.Datetime, 128)})
		}
	}
	if len(resources) > 1000 {
		resources = resources[:1000]
	}
	tx, err := a.db.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, errors.New("追新索引写入失败")
	}
	defer tx.Rollback()
	newCount := 0
	now := time.Now().Unix()
	for _, resource := range resources {
		if resource.Title == "" {
			resource.Title = s.Title
		}
		var previous, status, previousData string
		err = tx.QueryRowContext(ctx, "SELECT fingerprint,status,data FROM feature_tracking_resources WHERE id=$1", resource.ID).Scan(&previous, &status, &previousData)
		if err == nil && trackingMobileIncomplete(resource) {
			var saved trackingResource
			if json.Unmarshal([]byte(previousData), &saved) == nil && saved.Title == resource.Title && saved.Source == resource.Source && saved.Published == resource.Published {
				if _, _, parseErr := trackingShareCode(saved, "139Yun"); parseErr == nil {
					resource.URL, resource.Password = saved.URL, saved.Password
				}
			}
		}
		raw := featureJSON(resource)
		fingerprint := digest(resource.Cloud + "\x00" + resource.Title + "\x00" + resource.Password)
		if errors.Is(err, sql.ErrNoRows) {
			_, err = tx.ExecContext(ctx, "INSERT INTO feature_tracking_resources(id,subscription,cloud,data,fingerprint,first_seen,updated,last_seen) VALUES($1,$2,$3,$4,$5,$6,$6,$6)", resource.ID, s.ID, resource.Cloud, raw, fingerprint, now)
			if err == nil {
				newCount++
			}
		} else if err == nil {
			changed := fingerprint != previous
			_, err = tx.ExecContext(ctx, "UPDATE feature_tracking_resources SET data=$1,fingerprint=$2,last_seen=$3,updated=CASE WHEN fingerprint<>$2 THEN $3 ELSE updated END,status=CASE WHEN fingerprint<>$2 AND status<>'ignored' THEN 'new' ELSE status END WHERE id=$4", raw, fingerprint, now, resource.ID)
			if err == nil && changed && status != "ignored" {
				newCount++
			}
		}
		if err != nil {
			return 0, 0, errors.New("追新索引写入失败，订阅可能已删除")
		}
	}
	pinned, pinErr := a.trackingImportState(s.ID)
	if pinErr != nil {
		return 0, 0, errors.New("追新入库状态读取失败")
	}
	_, err = tx.ExecContext(ctx, "DELETE FROM feature_tracking_resources WHERE id IN (SELECT id FROM feature_tracking_resources WHERE subscription=$1 AND status<>'new' AND id<>$2 AND id<>$3 ORDER BY updated DESC,id OFFSET 2000)", s.ID, s.AutoImport.ResourceID, pinned.ResourceID)
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		return 0, 0, errors.New("追新索引保存失败")
	}
	return len(resources), newCount, nil
}
func trackingContains(values []string, s string) bool {
	for _, v := range values {
		if v == s {
			return true
		}
	}
	return false
}

type trackingJob struct {
	IDs    []string
	Manual *trackingSubscription
}

func (a *App) trackingQueue(ids []string) bool {
	a.features.trackingMu.Lock()
	defer a.features.trackingMu.Unlock()
	if a.features.ctx.Err() != nil || a.features.trackingBusy {
		return false
	}
	a.features.trackingBusy = true
	select {
	case a.features.trackingQueue <- trackingJob{IDs: ids}:
		return true
	default:
		a.features.trackingBusy = false
		return false
	}
}
func (a *App) trackingBackground(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case job := <-a.features.trackingQueue:
			if job.Manual != nil {
				a.trackingManualImport(ctx, *job.Manual)
			} else {
				a.trackingRun(ctx, job.IDs)
			}
		case <-ticker.C:
			c := a.trackingConfig()
			if !c.Enabled || c.URL == "" {
				continue
			}
			rows, err := a.db.Query("SELECT id,data FROM feature_tracking_subscriptions WHERE next_search<=? ORDER BY next_search,id", time.Now().Unix())
			if err != nil {
				continue
			}
			ids := []string{}
			for rows.Next() {
				var sid, raw string
				if err = rows.Scan(&sid, &raw); err != nil {
					break
				}
				var s trackingSubscription
				if json.Unmarshal([]byte(raw), &s) == nil && s.Enabled {
					ids = append(ids, sid)
				}
			}
			if err == nil {
				err = rows.Err()
			}
			rows.Close()
			if err == nil && len(ids) > 0 {
				a.trackingQueue(ids)
			}
		}
	}
}
func (a *App) trackingRun(parent context.Context, ids []string) {
	defer func() { a.features.trackingMu.Lock(); a.features.trackingBusy = false; a.features.trackingMu.Unlock() }()
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	task := a.newActivity("tracking", "", "追新索引")
	a.features.mu.Lock()
	a.features.jobs["tracking:"+task] = cancel
	a.features.mu.Unlock()
	defer func() { a.features.mu.Lock(); delete(a.features.jobs, "tracking:"+task); a.features.mu.Unlock() }()
	a.changeActivity(task, func(v *activityEntry) {
		v.State = "running"
		v.Total = len(ids)
		v.Current = "正在搜索订阅作品"
	})
	c := a.trackingConfig()
	failed := 0
	for index, sid := range ids {
		if ctx.Err() != nil {
			a.finishActivity(task, ctx.Err())
			return
		}
		var raw string
		if a.db.QueryRow("SELECT data FROM feature_tracking_subscriptions WHERE id=?", sid).Scan(&raw) != nil {
			continue
		}
		var s trackingSubscription
		if json.Unmarshal([]byte(raw), &s) != nil {
			continue
		}
		_, err := a.db.Exec("UPDATE feature_tracking_subscriptions SET state='running',error='' WHERE id=?", sid)
		if err != nil {
			failed++
			continue
		}
		a.changeActivity(task, func(v *activityEntry) { v.Current = "搜索 · " + s.Title; v.Done = index })
		count, added, err := a.trackingSearch(ctx, c, s)
		if s.AutoImport.Enabled && ctx.Err() == nil {
			importErr := a.trackingAutoImport(ctx, s, task)
			if importErr != nil {
				failed++
			}
		}
		now := time.Now().Unix()
		state, reason := "complete", ""
		if err != nil {
			state = "error"
			reason = err.Error()
			failed++
		}
		if ctx.Err() != nil {
			state = "interrupted"
			reason = "追新搜索已取消"
		}
		if _, saveErr := a.db.Exec("UPDATE feature_tracking_subscriptions SET state=?,error=?,last_search=?,next_search=?,last_count=CASE WHEN ?='complete' THEN ? ELSE last_count END WHERE id=?", state, reason, now, now+int64(max(15, s.Minutes))*60, state, count, sid); saveErr != nil {
			failed++
		}
		a.changeActivity(task, func(v *activityEntry) {
			v.Done = index + 1
			v.Current = fmt.Sprintf("%s · 找到 %d 条 · 新增或更新 %d 条 · 失败 %d", s.Title, count, added, failed)
		})
	}
	if ctx.Err() != nil {
		a.finishActivity(task, ctx.Err())
	} else if failed > 0 {
		a.finishActivity(task, fmt.Errorf("%d 项搜索或自动入库失败，请在追新索引查看原因", failed))
	} else {
		a.finishActivity(task, nil)
	}
}
