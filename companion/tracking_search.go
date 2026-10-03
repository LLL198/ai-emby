package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

func (a *App) trackingDirectSearchAPI(w http.ResponseWriter, r *http.Request) {
	if !featureMethod(w, r, "GET", "POST") {
		return
	}
	sid := r.URL.Query().Get("ID")
	if r.Method == "POST" {
		var b struct {
			Title, Source string
			Year          int
			CloudTypes    []string
		}
		if !body(w, r, &b) {
			return
		}
		b.Title = strings.TrimSpace(b.Title)
		if b.Title == "" || len(b.Title) > 512 || b.Year < 0 || b.Year > 9999 {
			fail(w, 400, "请填写作品名称和有效年份")
			return
		}
		if b.Source == "" {
			b.Source = "all"
		}
		if b.Source != "all" && b.Source != "plugin" && b.Source != "tg" {
			fail(w, 400, "搜索来源无效")
			return
		}
		if len(b.CloudTypes) > len(trackingClouds) {
			fail(w, 400, "网盘类型无效")
			return
		}
		clouds := []string{}
		for _, cloud := range b.CloudTypes {
			if trackingClouds[cloud] == "" {
				fail(w, 400, "网盘类型无效")
				return
			}
			if !trackingContains(clouds, cloud) {
				clouds = append(clouds, cloud)
			}
		}
		sort.Strings(clouds)
		c := a.trackingConfig()
		if c.URL == "" {
			fail(w, 400, "请先配置 PanSou 服务地址")
			return
		}
		if !a.features.trackingSearchMu.TryLock() {
			fail(w, 409, "正在搜索资源，请等待搜索结束")
			return
		}
		defer a.features.trackingSearchMu.Unlock()
		s := trackingSubscription{Title: b.Title, Year: b.Year, Query: b.Title, Source: b.Source, CloudTypes: clouds}
		if s.Year > 0 {
			s.Query += " " + strconv.Itoa(s.Year)
		}
		s.ID = "search-" + digest(featureJSON(s))
		sid = s.ID
		now := time.Now().Unix()
		_, err := a.db.Exec(`INSERT INTO feature_tracking_subscriptions(id,data,created,context_kind,state)
VALUES(?,?,?,'search','running') ON CONFLICT(id) DO UPDATE SET created=excluded.created,state='running',error='' WHERE feature_tracking_subscriptions.context_kind='search'`, sid, featureJSON(s), now)
		if err != nil {
			featureError(w, err)
			return
		}
		count, _, searchErr := a.trackingSearch(r.Context(), c, s)
		if searchErr != nil {
			_, _ = a.db.Exec("UPDATE feature_tracking_subscriptions SET state='error',error=? WHERE id=? AND context_kind='search'", searchErr.Error(), sid)
			fail(w, 502, searchErr.Error())
			return
		}
		if _, err = a.db.Exec("UPDATE feature_tracking_subscriptions SET state='complete',error='',last_search=?,last_count=? WHERE id=? AND context_kind='search'", now, count, sid); err != nil {
			featureError(w, err)
			return
		}
	}
	if len(sid) > 128 {
		fail(w, 400, "搜索编号无效")
		return
	}
	var session trackingRecord
	var raw string
	query := "SELECT id,data,created,last_search,state,error,last_count FROM feature_tracking_subscriptions WHERE context_kind='search'"
	args := []any{}
	if sid != "" {
		query += " AND id=?"
		args = append(args, sid)
	}
	query += " ORDER BY created DESC,id LIMIT 1"
	err := a.db.QueryRow(query, args...).Scan(&session.ID, &raw, &session.Created, &session.LastSearch, &session.State, &session.Error, &session.LastCount)
	if errors.Is(err, sql.ErrNoRows) && sid == "" {
		respond(w, M{"Session": nil, "Items": []trackingResource{}})
		return
	}
	if errors.Is(err, sql.ErrNoRows) {
		fail(w, 404, "搜索记录不存在，请重新搜索")
		return
	}
	if err == nil {
		err = json.Unmarshal([]byte(raw), &session.trackingSubscription)
	}
	if err != nil {
		featureError(w, err)
		return
	}
	session.Import, err = a.trackingImportState(session.ID)
	if err != nil {
		featureError(w, err)
		return
	}
	session.Import.PendingFiles, session.Import.PendingParent, session.Import.ConfigKey = nil, "", ""
	if session.Import.PendingTask != "" {
		session.Import.PendingTask = "pending"
	}
	rows, err := a.db.Query("SELECT id,data,status,first_seen,updated FROM feature_tracking_resources WHERE subscription=? AND last_seen>=? ORDER BY cloud,updated DESC,id LIMIT 1000", session.ID, session.LastSearch)
	if err != nil {
		featureError(w, err)
		return
	}
	defer rows.Close()
	items := []trackingResource{}
	for rows.Next() {
		var resource trackingResource
		var rid, status string
		var first, updated int64
		if err = rows.Scan(&rid, &raw, &status, &first, &updated); err != nil {
			break
		}
		if err = json.Unmarshal([]byte(raw), &resource); err != nil {
			break
		}
		resource.ID, resource.Subscription, resource.Status = rid, session.ID, status
		resource.FirstSeen, resource.Updated = first, updated
		items = append(items, resource)
	}
	if err == nil {
		err = rows.Err()
	}
	if err != nil {
		featureError(w, err)
		return
	}
	respond(w, M{"Session": session, "Items": items})
}
