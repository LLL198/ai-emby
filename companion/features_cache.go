package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type featureCacheEntry struct {
	ID, Name, Item string
	Size           int64
	Updated        time.Time
	Active         bool
	Error          string
}

func featureDirectorySize(path string) int64 {
	var size int64
	_ = filepath.WalkDir(path, func(_ string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if i, e := d.Info(); e == nil && i.Mode().IsRegular() {
				size += i.Size()
			}
		}
		return nil
	})
	return size
}
func (a *App) featureCacheEntries() []featureCacheEntry {
	dirs, _ := os.ReadDir(filepath.Join(featureDataRoot(), "playback"))
	entries := []featureCacheEntry{}
	for _, dir := range dirs {
		if !dir.IsDir() || len(dir.Name()) != 40 {
			continue
		}
		path := filepath.Join(featureDataRoot(), "playback", dir.Name())
		var rec featureTranscode
		data, _ := os.ReadFile(filepath.Join(path, "record.json"))
		_ = json.Unmarshal(data, &rec)
		info, _ := dir.Info()
		if info == nil {
			continue
		}
		entry := featureCacheEntry{ID: dir.Name(), Name: rec.Name, Item: rec.Item, Size: featureDirectorySize(path), Updated: info.ModTime(), Error: rec.Error}
		if !rec.Used.IsZero() {
			entry.Updated = rec.Used
		}
		a.features.cacheMu.Lock()
		if job := a.features.transcodes[entry.ID]; job != nil {
			entry.Active = !job.Done || time.Since(job.Used) < 2*time.Minute
			entry.Name = job.Name
			entry.Item = job.Item
			entry.Updated = job.Used
			entry.Error = job.Error
		}
		a.features.cacheMu.Unlock()
		a.features.playMu.Lock()
		for _, p := range a.features.plays {
			if p.Item == entry.Item && time.Since(p.At) < 2*time.Minute {
				entry.Active = true
			}
		}
		a.features.playMu.Unlock()
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Updated.Before(entries[j].Updated) })
	return entries
}
func (a *App) featureDeleteCache(id string) bool {
	if len(id) != 40 || strings.Trim(id, "0123456789abcdef") != "" {
		return false
	}
	a.features.cacheMu.Lock()
	defer a.features.cacheMu.Unlock()
	if job := a.features.transcodes[id]; job != nil && (!job.Done || time.Since(job.Used) < 2*time.Minute) {
		return false
	}
	var item string
	if job := a.features.transcodes[id]; job != nil {
		item = job.Item
	} else {
		var rec featureTranscode
		data, _ := os.ReadFile(filepath.Join(featureDataRoot(), "playback", id, "record.json"))
		_ = json.Unmarshal(data, &rec)
		item = rec.Item
	}
	a.features.playMu.Lock()
	playing := false
	for _, p := range a.features.plays {
		if p.Item == item && time.Since(p.At) < 2*time.Minute {
			playing = true
		}
	}
	a.features.playMu.Unlock()
	if playing {
		return false
	}
	if err := os.RemoveAll(filepath.Join(featureDataRoot(), "playback", id)); err != nil {
		return false
	}
	delete(a.features.transcodes, id)
	return true
}
func (a *App) featureCacheAPI(w http.ResponseWriter, r *http.Request) {
	if !featureMethod(w, r, http.MethodGet, http.MethodDelete) {
		return
	}
	entries := a.featureCacheEntries()
	if r.Method == http.MethodDelete {
		var request struct {
			IDs []string
			All bool
		}
		if !body(w, r, &request) {
			return
		}
		wanted := map[string]bool{}
		for _, id := range request.IDs {
			wanted[id] = true
		}
		removed, skipped := 0, 0
		for _, entry := range entries {
			if request.All || wanted[entry.ID] {
				if !entry.Active && a.featureDeleteCache(entry.ID) {
					removed++
				} else {
					skipped++
				}
			}
		}
		respond(w, M{"Removed": removed, "Protected": skipped})
		return
	}
	size := int64(0)
	for _, e := range entries {
		size += e.Size
	}
	respond(w, M{"Items": entries, "Size": size, "Limit": int64(a.featurePlaybackConfig().CacheGB) << 30, "MetadataSize": featureDirectorySize(filepath.Join(featureDataRoot(), "metadata")), "DanmakuSize": featureDirectorySize(filepath.Join(featureDataRoot(), "danmaku"))})
}
func (a *App) featureCleanup(ctx context.Context) {
	c := a.featurePlaybackConfig()
	entries := a.featureCacheEntries()
	size := int64(0)
	for _, e := range entries {
		size += e.Size
	}
	for _, e := range entries {
		if ctx.Err() != nil {
			return
		}
		if !e.Active && (time.Since(e.Updated) > time.Duration(c.RetentionDays)*24*time.Hour || size > int64(c.CacheGB)<<30) {
			if a.featureDeleteCache(e.ID) {
				size -= e.Size
			}
		}
	}
	_, _ = a.db.Exec("DELETE FROM feature_tasks WHERE updated<? AND state NOT IN ('waiting','queued','running','counting','cleaning','paused')", time.Now().Add(-90*24*time.Hour).UnixNano())
}
