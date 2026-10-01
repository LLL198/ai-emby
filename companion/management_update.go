package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func (a *App) createLibrary(w http.ResponseWriter, name, kind, path string, paths []string) {
	a.libraryConfig.Lock()
	defer a.libraryConfig.Unlock()
	if len(paths) == 0 {
		paths = []string{path}
	}
	if strings.TrimSpace(name) == "" || len(paths) > 100 {
		fail(w, 400, "请输入名称，目录最多100个")
		return
	}
	validated := []string{}
	for _, p := range paths {
		real, e := filepath.EvalSymlinks(mediaPath(p))
		if e != nil || !allowedMediaPath(real) {
			fail(w, 400, "请选择已挂载媒体目录下的现有目录")
			return
		}
		fi, e := os.Stat(real)
		if e != nil || !fi.IsDir() {
			fail(w, 400, "无效目录")
			return
		}
		for _, prev := range validated {
			if pathsOverlap(real, prev) {
				fail(w, 409, "目录不能重复或互相包含")
				return
			}
		}
		for _, lib := range a.libraries() {
			for _, prev := range lib["Locations"].([]string) {
				if pathsOverlap(real, prev) {
					fail(w, 409, "媒体库目录不能重复或互相包含")
					return
				}
			}
		}
		validated = append(validated, real)
	}
	if kind != "tvshows" {
		kind = "movies"
	}
	key := id()
	tx, e := a.db.Begin()
	if e != nil {
		fail(w, 500, "事务失败")
		return
	}
	defer tx.Rollback()
	_, e = tx.Exec("INSERT INTO libraries(id,name,path,kind) VALUES(?,?,?,?)", key, name, validated[0], kind)
	raw, _ := json.Marshal(validated)
	if e == nil {
		_, e = tx.Exec("INSERT INTO settings(k,v) VALUES(?,?)", "library-paths:"+key, string(raw))
	}
	if e == nil {
		e = tx.Commit()
	}
	if e != nil {
		fail(w, 409, "媒体库保存失败")
		return
	}
	go a.scanLibrary(key)
	respond(w, M{"Id": key, "queued": true, "Message": "已加入队列，请在实时日志中查看"})
}

type scanSchedule struct {
	Enabled   bool
	Frequency string
	Time      string
	Weekday   int
	Minutes   int
	Next      int64
}

func (a *App) getScanSchedule() scanSchedule {
	c := scanSchedule{Frequency: "daily", Time: "03:00", Minutes: 60}
	var raw string
	if a.db.QueryRow("SELECT v FROM settings WHERE k='full_scan_schedule'").Scan(&raw) == nil {
		json.Unmarshal([]byte(raw), &c)
	}
	return c
}
func nextScan(c scanSchedule, now time.Time) time.Time {
	if c.Frequency == "minutes" {
		return now.Add(time.Duration(c.Minutes) * time.Minute)
	}
	t, _ := time.Parse("15:04", c.Time)
	n := time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), 0, 0, time.UTC)
	if !n.After(now) {
		n = n.AddDate(0, 0, 1)
	}
	if c.Frequency == "weekly" {
		for int(n.Weekday()) != c.Weekday {
			n = n.AddDate(0, 0, 1)
		}
	}
	return n
}
func (a *App) saveScanSchedule(c scanSchedule) error {
	raw, _ := json.Marshal(c)
	_, e := a.db.Exec("INSERT INTO settings(k,v) VALUES('full_scan_schedule',?) ON CONFLICT(k) DO UPDATE SET v=excluded.v", string(raw))
	return e
}
func (a *App) scanScheduleAPI(w http.ResponseWriter, r *http.Request) {
	a.libraryConfig.Lock()
	defer a.libraryConfig.Unlock()
	if r.Method == "GET" {
		respond(w, a.getScanSchedule())
		return
	}
	if r.Method != "PUT" {
		fail(w, 405, "method")
		return
	}
	var c scanSchedule
	if !body(w, r, &c) {
		return
	}
	_, e := time.Parse("15:04", c.Time)
	if (c.Frequency != "daily" && c.Frequency != "weekly" && c.Frequency != "minutes") || e != nil || c.Weekday < 0 || c.Weekday > 6 || c.Minutes < 1 || c.Minutes > 10080 {
		fail(w, 400, "无效定时时间")
		return
	}
	c.Next = nextScan(c, time.Now().UTC()).Unix()
	if e = a.saveScanSchedule(c); e != nil {
		fail(w, 500, "保存失败")
		return
	}
	respond(w, c)
}
func (a *App) runScanSchedule() {
	tick := time.NewTicker(15 * time.Second)
	defer tick.Stop()
	for now := range tick.C {
		a.libraryConfig.Lock()
		c := a.getScanSchedule()
		due := c.Enabled && c.Next > 0 && now.Unix() >= c.Next
		if due {
			c.Next = nextScan(c, now.UTC()).Unix()
			if a.saveScanSchedule(c) != nil {
				due = false
			}
		}
		a.libraryConfig.Unlock()
		if due {
			a.jobs.mu.Lock()
			busy := len(a.jobs.pending) > 0 || a.jobs.running[0] > 0
			a.jobs.mu.Unlock()
			if !busy {
				a.scanAll()
			}
		}
	}
}
func (a *App) authWarning(r *http.Request, name, reason string) {
	// Avoid amplifying repeated unauthenticated requests into unbounded log work.
	a.activity.mu.Lock()
	for i := len(a.activity.entries) - 1; i >= 0; i-- {
		v := a.activity.entries[i]
		if v.Category == "warning" && v.Username == name && v.IP == r.RemoteAddr && v.Error == reason && time.Since(v.Updated) < time.Minute {
			v.Updated = time.Now()
			a.activity.mu.Unlock()
			return
		}
	}
	a.activity.mu.Unlock()
	key := a.newActivity("warning", "", "用户验证失败")
	a.changeActivity(key, func(v *activityEntry) {
		v.State = "error"
		v.Username = name
		v.IP = r.RemoteAddr
		v.Device = device(r)
		v.Client = r.UserAgent()
		v.Error = reason
	})
}
func (a *App) playbackProgress(u User, item string, position, duration int64) {
	if duration <= 0 {
		var raw string
		if a.db.QueryRow("SELECT data FROM media_probe WHERE item=?", item).Scan(&raw) == nil {
			var m struct{ RunTimeTicks int64 }
			json.Unmarshal([]byte(raw), &m)
			duration = m.RunTimeTicks
		}
	}
	if duration <= 0 {
		if x, e := a.item(item); e == nil {
			m := M{}
			a.enrichMedia(x, a.metadata(x), m)
			switch n := m["RunTimeTicks"].(type) {
			case int64:
				duration = n
			case float64:
				duration = int64(n)
			}
		}
	}
	a.activity.mu.Lock()
	defer a.activity.mu.Unlock()
	for _, v := range a.activity.entries {
		if v.Category == "playback" && v.Username == u.Name && v.DeviceKey == u.Device && v.ItemID == item && v.State == "requested" {
			v.PositionTicks = max(position, 0)
			if duration > 0 {
				v.RunTimeTicks = duration
			}
			if v.RunTimeTicks > 0 {
				v.Progress = min(100, float64(v.PositionTicks)*100/float64(v.RunTimeTicks))
			}
			v.Updated = time.Now()
		}
	}
}
