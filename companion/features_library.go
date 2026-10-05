package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type featureLibraryPolicy struct {
	Watch                bool
	AutoMetadata         bool
	Probe                bool
	Capture              bool
	Intro                bool
	Participate          bool
	RefreshMissingImages bool
	WriteNFO             bool
	WriteArtwork         bool
	RestrictUsers        bool
	Users                []string
	SortBy               string
	SortOrder            string
	FileConcurrency      int
}

type featureSchedule struct {
	Enabled            bool
	Frequency          string
	Time               string
	Weekday            int
	Minutes            int
	Mode               string
	Next               int64
	Last               int64
	DeferPlayback      bool
	MaxDeferralMinutes int
	DeferredSince      int64
	Cleanup            bool
}

func (a *App) featurePolicy(lib string) featureLibraryPolicy {
	policy := featureLibraryPolicy{Participate: true, SortBy: "DateCreated", SortOrder: "Descending", Users: []string{}}
	var raw string
	if a.db.QueryRow("SELECT data FROM feature_library_policy WHERE lib=?", lib).Scan(&raw) == nil {
		_ = json.Unmarshal([]byte(raw), &policy)
	}
	return policy
}

func (a *App) featureLibraryAPI(w http.ResponseWriter, r *http.Request) {
	if !featureMethod(w, r, http.MethodGet, http.MethodPut) {
		return
	}
	if r.Method == http.MethodGet {
		libraries := a.libraries()
		for _, lib := range libraries {
			lib["Policy"] = a.featurePolicy(lib["Id"].(string))
		}
		respond(w, libraries)
		return
	}
	var request struct {
		ID     string
		Policy featureLibraryPolicy
	}
	if !body(w, r, &request) {
		return
	}
	var exists string
	if err := a.db.QueryRow("SELECT id FROM libraries WHERE id=?", request.ID).Scan(&exists); err != nil {
		featureError(w, err)
		return
	}
	p := request.Policy
	if p.FileConcurrency < 0 || p.FileConcurrency > 32 || len(p.Users) > 1000 {
		fail(w, 400, "单库并发范围为 0–32，0 表示使用全局设置")
		return
	}
	if p.SortBy != "DateCreated" && p.SortBy != "SortName" && p.SortBy != "ProductionYear" && p.SortBy != "PremiereDate" {
		fail(w, 400, "无效排序方式")
		return
	}
	if p.SortOrder != "Ascending" && p.SortOrder != "Descending" {
		fail(w, 400, "无效排序方向")
		return
	}
	for _, id := range p.Users {
		var uid string
		if a.db.QueryRow("SELECT id FROM users WHERE id=?", id).Scan(&uid) != nil {
			fail(w, 400, "授权用户不存在")
			return
		}
	}
	_, err := a.db.Exec("INSERT INTO feature_library_policy(lib,data) VALUES(?,?) ON CONFLICT(lib) DO UPDATE SET data=excluded.data", request.ID, featureJSON(p))
	if err != nil {
		featureError(w, err)
		return
	}
	respond(w, M{"ID": request.ID, "Policy": p})
}

func (a *App) featureScheduleSettings() featureSchedule {
	c := featureSchedule{Frequency: "daily", Time: "03:00", Minutes: 60, Mode: "update", DeferPlayback: true, MaxDeferralMinutes: 120, Cleanup: true}
	a.featureSetting("schedule", &c)
	return c
}

func (a *App) featureScheduleAPI(w http.ResponseWriter, r *http.Request) {
	if !featureMethod(w, r, http.MethodGet, http.MethodPut, http.MethodPost) {
		return
	}
	c := a.featureScheduleSettings()
	if r.Method == http.MethodGet {
		respond(w, c)
		return
	}
	if r.Method == http.MethodPost {
		count := a.featureQueueLibraries(c.Mode)
		respond(w, M{"Queued": count})
		return
	}
	var next featureSchedule
	if !body(w, r, &next) {
		return
	}
	if _, err := time.Parse("15:04", next.Time); err != nil || (next.Frequency != "daily" && next.Frequency != "weekly" && next.Frequency != "minutes") || next.Minutes < 1 || next.Minutes > 10080 || next.Weekday < 0 || next.Weekday > 6 || next.MaxDeferralMinutes < 0 || next.MaxDeferralMinutes > 1440 || (next.Mode != "scan" && next.Mode != "update") {
		fail(w, 400, "请检查执行周期、时间和推迟上限")
		return
	}
	next.Last = c.Last
	next.Next = nextScan(scanSchedule{Frequency: next.Frequency, Time: next.Time, Weekday: next.Weekday, Minutes: next.Minutes}, time.Now().UTC()).Unix()
	if err := a.saveFeatureSetting("schedule", next); err != nil {
		featureError(w, err)
		return
	}
	if next.Enabled {
		// One scheduler owns automatic work. Existing manual scan APIs remain available.
		legacy := a.getScanSchedule()
		legacy.Enabled = false
		if err := a.saveScanSchedule(legacy); err != nil {
			featureError(w, err)
			return
		}
	}
	respond(w, next)
}

func (a *App) featureQueueLibraries(mode string) int {
	rows, err := a.db.Query("SELECT id FROM libraries ORDER BY id")
	if err != nil {
		return 0
	}
	ids := []string{}
	for rows.Next() {
		var lib string
		if rows.Scan(&lib) == nil {
			ids = append(ids, lib)
		}
	}
	rows.Close()
	queued := 0
	for _, lib := range ids {
		if !a.featurePolicy(lib).Participate {
			continue
		}
		if _, ok := a.reserveConcurrentScan(lib); ok {
			queued++
			go a.runConcurrentScan(lib, mode != "scan", false, nil)
		}
	}
	return queued
}

func (a *App) featureIsPlaying() bool {
	var count int
	_ = a.db.QueryRow("SELECT count(*) FROM plays WHERE updated>?", time.Now().Add(-90*time.Second).Unix()).Scan(&count)
	return count > 0
}

func (a *App) featureBackground(ctx context.Context) {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	lastMonitor := time.Time{}
	stamps := map[string]string{}
	for {
		select {
		case <-ctx.Done():
			a.featurePersistActivities()
			return
		case now := <-ticker.C:
			a.featurePersistActivities()
			c := a.featureScheduleSettings()
			if c.Enabled && c.Next > 0 && now.Unix() >= c.Next {
				if c.DeferPlayback && a.featureIsPlaying() && (c.DeferredSince == 0 || now.Unix()-c.DeferredSince < int64(c.MaxDeferralMinutes)*60) {
					if c.DeferredSince == 0 {
						c.DeferredSince = now.Unix()
						_ = a.saveFeatureSetting("schedule", c)
					}
				} else {
					c.Last = now.Unix()
					c.DeferredSince = 0
					c.Next = nextScan(scanSchedule{Frequency: c.Frequency, Time: c.Time, Weekday: c.Weekday, Minutes: c.Minutes}, now.UTC()).Unix()
					if a.saveFeatureSetting("schedule", c) == nil {
						a.featureQueueLibraries(c.Mode)
					}
				}
			}
			if now.Sub(lastMonitor) >= time.Minute {
				a.expirePlaybackTasks()
				a.loadProxySettings()
				lastMonitor = now
				a.featureMonitorLibraries(ctx, stamps)
				if c.Cleanup {
					a.featureCleanup(ctx)
				}
			}
		}
	}
}

func (a *App) featureMonitorLibraries(ctx context.Context, stamps map[string]string) {
	for _, lib := range a.libraries() {
		id, _ := lib["Id"].(string)
		p := a.featurePolicy(id)
		if p.RefreshMissingImages && !a.featureIsPlaying() {
			a.featureQueueImageRefresh(id)
		}
		if !p.Watch {
			delete(stamps, id)
			continue
		}
		parts := []string{}
		for _, root := range lib["Locations"].([]string) {
			count, mtime := int64(0), int64(0)
			err := boundedWalk(root, func(path string, entry os.DirEntry, walkErr error) error {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				if walkErr != nil {
					return walkErr
				}
				if entry.IsDir() {
					if strings.HasPrefix(entry.Name(), ".") && path != root {
						return filepath.SkipDir
					}
					return nil
				}
				if !featureMediaExtension(path) {
					return nil
				}
				info, err := entry.Info()
				if err != nil {
					return err
				}
				count++
				mtime += info.ModTime().UnixNano() + info.Size()
				return nil
			})
			if err != nil {
				parts = nil
				break
			}
			parts = append(parts, strconv.FormatInt(count, 10)+":"+strconv.FormatInt(mtime, 10))
		}
		if len(parts) == 0 {
			continue
		}
		stamp := strings.Join(parts, "|")
		previous, had := stamps[id]
		stamps[id] = stamp
		if had && previous != stamp {
			if _, ok := a.reserveConcurrentScan(id); ok {
				go a.runConcurrentScan(id, true, false, nil)
			}
		}
	}
}

func featureMediaExtension(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".strm", ".mkv", ".mp4", ".m4v", ".mov", ".avi", ".webm", ".ts", ".m2ts", ".iso":
		return true
	}
	return false
}

func (a *App) featureLibraryAction(w http.ResponseWriter, r *http.Request) {
	if !featureMethod(w, r, http.MethodPost) {
		return
	}
	var request struct{ ID, Action string }
	if !body(w, r, &request) {
		return
	}
	var lib string
	if err := a.db.QueryRow("SELECT id FROM libraries WHERE id=?", request.ID).Scan(&lib); err != nil {
		featureError(w, err)
		return
	}
	if request.Action == "scan" || request.Action == "update" {
		if _, ok := a.reserveConcurrentScan(lib); !ok {
			fail(w, 409, "这个媒体库已有扫描任务")
			return
		}
		go a.runConcurrentScan(lib, request.Action == "update", false, nil)
		respond(w, M{"Queued": true})
		return
	}
	if request.Action != "metadata" && request.Action != "probe" && request.Action != "capture" && request.Action != "intro" && request.Action != "images" {
		fail(w, 400, "无效媒体库任务")
		return
	}
	task, err := a.featureStartLibraryJob(lib, request.Action, token(r), r.UserAgent())
	if err != nil {
		fail(w, 409, err.Error())
		return
	}
	respond(w, M{"ID": task, "Queued": true})
}

func (a *App) featureStartLibraryJob(lib, action, authToken, ua string) (string, error) {
	key := lib + ":" + action
	a.features.mu.Lock()
	if a.features.jobs[key] != nil {
		a.features.mu.Unlock()
		return "", errors.New("同类型任务已在运行")
	}
	ctx, cancel := context.WithCancel(a.features.ctx)
	a.features.jobs[key] = cancel
	a.features.mu.Unlock()
	labels := map[string]string{"metadata": "补全作品资料", "probe": "提取媒体信息", "capture": "补全封面截帧", "intro": "读取片头片尾章节", "images": "补全近期单集图片"}
	task := a.newActivity("library-"+action, lib, labels[action])
	a.features.wg.Add(1)
	go func() {
		defer a.features.wg.Done()
		defer cancel()
		defer func() { a.features.mu.Lock(); delete(a.features.jobs, key); a.features.mu.Unlock() }()
		err := a.featureProcessLibrary(ctx, lib, action, authToken, ua, task)
		a.finishActivity(task, err)
	}()
	return task, nil
}

func (a *App) featureAfterScan(lib string) {
	p := a.featurePolicy(lib)
	for _, entry := range []struct {
		enabled bool
		action  string
	}{{p.AutoMetadata, "metadata"}, {p.Probe, "probe"}, {p.Capture, "capture"}, {p.Intro, "intro"}} {
		if entry.enabled {
			_, _ = a.featureStartLibraryJob(lib, entry.action, "", "")
		}
	}
}

func (a *App) featureQueueImageRefresh(lib string) {
	var last int64
	a.featureSetting("image-refresh:"+lib, &last)
	if featureNow()-last < 86400 {
		return
	}
	if _, err := a.featureStartLibraryJob(lib, "images", "", ""); err == nil {
		_ = a.saveFeatureSetting("image-refresh:"+lib, featureNow())
	}
}
