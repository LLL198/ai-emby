package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type ProbeSettings struct {
	Browse      bool
	PreloadNext bool
	Persistent  bool
	Directory   string
	Concurrency int
}
type probeJob struct {
	x                   Item
	token, ua, activity string
	done                chan struct{}
	data                M
	err                 error
	autoNext            bool
	cancel              context.CancelFunc
}
type probeState struct {
	mu      sync.Mutex
	storage sync.Mutex
	jobs    map[string]*probeJob
	queue   []*probeJob
	running int
	next    map[string]time.Time
}

func mediaInfoRoot() string {
	if p := os.Getenv("MEDIA_INFO_ROOT"); p != "" {
		return p
	}
	return "/app/data"
}
func (a *App) probeSettings() ProbeSettings {
	c := ProbeSettings{Browse: true, PreloadNext: true, Directory: filepath.Join(mediaInfoRoot(), "media-info"), Concurrency: 1}
	var raw string
	if a.db.QueryRow("SELECT v FROM settings WHERE k='media_info'").Scan(&raw) == nil {
		json.Unmarshal([]byte(raw), &c)
	}
	if c.Concurrency < 1 {
		c.Concurrency = 1
	}
	if c.Concurrency > 16 {
		c.Concurrency = 16
	}
	return c
}
func (a *App) mediaSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" {
		respond(w, a.probeSettings())
		return
	}
	if r.Method != "PUT" {
		fail(w, 405, "PUT required")
		return
	}
	c := a.probeSettings()
	if !body(w, r, &c) {
		return
	}
	if c.Concurrency < 1 || c.Concurrency > 16 {
		fail(w, 400, "媒体信息提取并发必须为 1–16 的整数，不能设置为 0")
		return
	}
	c.Directory = filepath.Clean(strings.TrimSpace(c.Directory))
	root, e := filepath.EvalSymlinks(mediaInfoRoot())
	if e != nil || !strings.HasPrefix(c.Directory, root+string(os.PathSeparator)) {
		fail(w, 400, "保存目录必须位于 "+mediaInfoRoot()+" 的子目录")
		return
	}
	a.probes.storage.Lock()
	defer a.probes.storage.Unlock()
	if e = os.MkdirAll(c.Directory, 0700); e != nil {
		fail(w, 400, "保存目录不可写")
		return
	}
	real, e := filepath.EvalSymlinks(c.Directory)
	if e != nil || !strings.HasPrefix(real, root+string(os.PathSeparator)) {
		fail(w, 400, "保存目录不能指向数据目录之外")
		return
	}
	c.Directory = real
	f, e := os.CreateTemp(c.Directory, ".write-test-")
	if e != nil {
		fail(w, 400, "保存目录不可写")
		return
	}
	f.Close()
	os.Remove(f.Name())
	old := a.probeSettings()
	if old.Directory != c.Directory {
		// Copy only our validated cache records. Keep originals until the setting commits.
		entries, _ := filepath.Glob(filepath.Join(old.Directory, "*.json"))
		for _, p := range entries {
			b, e := os.ReadFile(p)
			if e != nil {
				fail(w, 500, "读取旧媒体信息失败")
				return
			}
			var rec mediaRecord
			if json.Unmarshal(b, &rec) != nil || rec.ItemID == "" || filepath.Base(p) != digest(rec.ItemID)+".json" {
				continue
			}
			if e = atomicMediaFile(c.Directory, rec); e != nil {
				fail(w, 500, "迁移媒体信息失败")
				return
			}
		}
	}
	b, _ := json.Marshal(c)
	if _, e = a.db.Exec("INSERT INTO settings VALUES('media_info',?) ON CONFLICT(k) DO UPDATE SET v=excluded.v", string(b)); e != nil {
		fail(w, 500, "保存设置失败")
		return
	}
	if old.Directory != c.Directory {
		entries, _ := filepath.Glob(filepath.Join(old.Directory, "*.json"))
		for _, p := range entries {
			var rec mediaRecord
			b, e := os.ReadFile(p)
			if e == nil && json.Unmarshal(b, &rec) == nil && rec.ItemID != "" && filepath.Base(p) == digest(rec.ItemID)+".json" {
				os.Remove(p)
			}
		}
	}
	a.probes.mu.Lock()
	for _, j := range a.probes.jobs {
		if !c.PreloadNext && j.autoNext && j.cancel != nil {
			j.cancel()
		}
	}
	a.dispatchProbesLocked(c.Concurrency)
	a.probes.mu.Unlock()
	respond(w, c)
}
func (a *App) queueProbe(x Item, t, ua string, force bool, automatic ...bool) *probeJob {
	if x.URL == "" {
		return nil
	}
	if !force {
		if m := a.cachedMedia(x); len(m) > 0 && m["Partial"] != true {
			j := &probeJob{done: make(chan struct{}), data: m}
			close(j.done)
			return j
		}
	}
	a.probes.mu.Lock()
	defer a.probes.mu.Unlock()
	if a.probes.jobs == nil {
		a.probes.jobs = map[string]*probeJob{}
	}
	key := digest(x.URL)
	if j := a.probes.jobs[key]; j != nil {
		return j
	}
	if len(a.probes.queue) >= 1000 {
		return nil
	}
	j := &probeJob{x: x, token: t, ua: ua, activity: a.newActivity("probe", x.ID, a.mediaLabel(x)), done: make(chan struct{})}
	j.autoNext = len(automatic) > 0 && automatic[0]
	a.probes.jobs[key] = j
	a.probes.queue = append(a.probes.queue, j)
	a.dispatchProbesLocked(a.probeSettings().Concurrency)
	return j
}
func (a *App) dispatchProbesLocked(limit int) {
	for a.probes.running < limit && len(a.probes.queue) > 0 {
		j := a.probes.queue[0]
		a.probes.queue[0] = nil
		a.probes.queue = a.probes.queue[1:]
		if len(a.probes.queue) == 0 {
			a.probes.queue = nil
		}
		ctx, cancel := context.WithCancel(context.Background())
		j.cancel = cancel
		if j.autoNext && !a.probeSettings().PreloadNext {
			cancel()
		}
		a.probes.running++
		a.changeActivity(j.activity, func(v *activityEntry) { v.State = "running" })
		go func() {
			if m := a.cachedMedia(j.x); len(m) > 0 && m["Partial"] != true {
				j.data = m
			} else {
				if ctx.Err() != nil {
					j.err = ctx.Err()
				} else {
					j.data, j.err = a.extractMedia(ctx, j.x, j.token, j.ua)
				}
			}
			cancel()
			a.finishActivity(j.activity, j.err)
			if j.autoNext && !a.probeSettings().PreloadNext {
				a.changeActivity(j.activity, func(v *activityEntry) { v.State = "cancelled"; v.Error = "" })
			}
			a.probes.mu.Lock()
			a.probes.running--
			key := digest(j.x.URL)
			if j.err == nil {
				delete(a.probes.jobs, key)
			} else {
				time.AfterFunc(30*time.Second, func() {
					a.probes.mu.Lock()
					if a.probes.jobs[key] == j {
						delete(a.probes.jobs, key)
					}
					a.probes.mu.Unlock()
				})
			}
			close(j.done)
			a.dispatchProbesLocked(a.probeSettings().Concurrency)
			a.probes.mu.Unlock()
		}()
	}
}

func (a *App) preloadNext(x Item, r *http.Request) {
	if x.Kind != "Episode" {
		return
	}
	parent, e := a.item(x.Parent)
	if e != nil {
		return
	}
	series := parent.ID
	if parent.Kind == "Season" {
		series = parent.Parent
	}
	n, e := readItem(a.db.QueryRow("SELECT "+cols+" FROM items WHERE kind='Episode' AND (parent=? OR parent IN (SELECT id FROM items WHERE parent=? AND kind='Season')) AND (season>? OR (season=? AND episode>?)) ORDER BY season,episode,id LIMIT 1", series, series, x.Season, x.Season, x.Episode))
	if e == nil {
		a.queueProbe(n, token(r), r.UserAgent(), false, true)
	}
}

type mediaRecord struct {
	ItemID, Source, Path string
	Data                 M
}

func atomicMediaFile(dir string, rec mediaRecord) error {
	if e := os.MkdirAll(dir, 0700); e != nil {
		return e
	}
	b, e := json.Marshal(rec)
	if e != nil {
		return e
	}
	f, e := os.CreateTemp(dir, ".media-")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if _, e = f.Write(b); e != nil {
		f.Close()
		return e
	}
	if e = f.Sync(); e != nil {
		f.Close()
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	return os.Rename(f.Name(), filepath.Join(dir, digest(rec.ItemID)+".json"))
}
func (a *App) saveMedia(x Item, m M) error {
	a.probes.storage.Lock()
	defer a.probes.storage.Unlock()
	if m["Partial"] == true {
		if existing := a.cachedMedia(x); len(existing) > 0 && existing["Partial"] != true {
			return nil
		}
	}
	current, e := a.item(x.ID)
	if e != nil || current.URL != x.URL {
		return fmt.Errorf("媒体已删除或源地址已变化")
	}
	c := a.probeSettings()
	if e = atomicMediaFile(c.Directory, mediaRecord{x.ID, digest(x.URL), x.Path, m}); e != nil {
		return e
	}
	b, _ := json.Marshal(m)
	_, e = a.db.Exec("INSERT INTO media_probe VALUES(?,?,?) ON CONFLICT(item) DO UPDATE SET source=excluded.source,data=excluded.data", x.ID, digest(x.URL), string(b))
	return e
}
func (a *App) archivedMedia(x Item, dir string) M {
	b, e := os.ReadFile(filepath.Join(dir, digest(x.ID)+".json"))
	if e != nil {
		return M{}
	}
	var rec mediaRecord
	if json.Unmarshal(b, &rec) != nil || rec.ItemID != x.ID || rec.Source != digest(x.URL) {
		return M{}
	}
	return rec.Data
}
func (a *App) cleanupMedia() error {
	a.probes.storage.Lock()
	defer a.probes.storage.Unlock()
	c := a.probeSettings()
	if c.Persistent {
		return nil
	}
	entries, e := filepath.Glob(filepath.Join(c.Directory, "*.json"))
	if e != nil {
		return e
	}
	for _, p := range entries {
		b, e := os.ReadFile(p)
		if e != nil {
			return e
		}
		var rec mediaRecord
		if json.Unmarshal(b, &rec) != nil || rec.ItemID == "" || filepath.Base(p) != digest(rec.ItemID)+".json" {
			continue
		}
		var exists int
		e = a.db.QueryRow("SELECT count(*) FROM items WHERE id=? AND url<>''", rec.ItemID).Scan(&exists)
		if e != nil {
			return e
		}
		missing := exists == 0
		if !missing {
			_, e = os.Stat(rec.Path)
			missing = os.IsNotExist(e)
			if missing {
				var root string
				if a.db.QueryRow("SELECT l.path FROM libraries l JOIN items i ON i.lib=l.id WHERE i.id=?", rec.ItemID).Scan(&root) != nil {
					continue
				}
				if st, err := os.Stat(root); err != nil || !st.IsDir() {
					continue
				}
			}
		}
		if missing {
			if e = os.Remove(p); e != nil && !os.IsNotExist(e) {
				return e
			}
			if _, e = a.db.Exec("DELETE FROM media_probe WHERE item=?", rec.ItemID); e != nil {
				return e
			}
		}
	}
	return nil
}
func (a *App) migrateMedia() error {
	after := ""
	for {
		rows, e := a.db.Query("SELECT p.item,p.source,i.path,p.data FROM media_probe p JOIN items i ON i.id=p.item WHERE p.item>? ORDER BY p.item LIMIT 100", after)
		if e != nil {
			return e
		}
		records := []mediaRecord{}
		n := 0
		for rows.Next() {
			var rec mediaRecord
			var raw string
			if e = rows.Scan(&rec.ItemID, &rec.Source, &rec.Path, &raw); e != nil {
				rows.Close()
				return e
			}
			after = rec.ItemID
			n++
			if json.Unmarshal([]byte(raw), &rec.Data) == nil {
				records = append(records, rec)
			}
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		if n == 0 {
			return nil
		}
		for _, rec := range records {
			if e = atomicMediaFile(a.probeSettings().Directory, rec); e != nil {
				return e
			}
		}
	}
}
