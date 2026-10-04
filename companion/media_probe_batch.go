package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

func (a *App) probeRootOptions() []M {
	options := []M{}
	for _, library := range a.libraries() {
		for _, root := range library["Locations"].([]string) {
			options = append(options, M{"Library": library["Id"], "Root": root, "Name": library["Name"]})
		}
	}
	return options
}

func (a *App) sanitizeProbeRoots(roots []probeRoot) []probeRoot {
	allowed := map[probeRoot]bool{}
	for _, option := range a.probeRootOptions() {
		allowed[probeRoot{option["Library"].(string), option["Root"].(string)}] = true
	}
	result := []probeRoot{}
	seen := map[probeRoot]bool{}
	for _, root := range roots {
		if allowed[root] && !seen[root] && filepath.IsAbs(root.Root) && filepath.Clean(root.Root) == root.Root {
			result = append(result, root)
			seen[root] = true
			if len(result) == 128 {
				break
			}
		}
	}
	return result
}

func (a *App) probeAutomation() probeAutomationSettings {
	a.mediaBatch.mu.Lock()
	defer a.mediaBatch.mu.Unlock()
	return a.probeAutomationLocked()
}

func (a *App) probeAutomationLocked() probeAutomationSettings {
	config := probeAutomationSettings{BatchRoots: []probeRoot{}, MonitorRoots: []probeRoot{}, State: "idle"}
	var raw string
	if a.db.QueryRow("SELECT v FROM settings WHERE k='media_probe_automation'").Scan(&raw) == nil {
		var saved probeAutomationSettings
		if json.Unmarshal([]byte(raw), &saved) == nil {
			config = saved
		}
	}
	config.BatchRoots = a.sanitizeProbeRoots(config.BatchRoots)
	config.MonitorRoots = a.sanitizeProbeRoots(config.MonitorRoots)
	if config.State == "" {
		config.State = "idle"
	}
	if config.State == "running" && !a.mediaBatch.running {
		config.State = "stopped"
	}
	return config
}

func (a *App) storeProbeAutomation(config probeAutomationSettings) error {
	data, err := json.Marshal(config)
	if err != nil {
		return err
	}
	_, err = a.db.Exec("INSERT INTO settings(k,v) VALUES('media_probe_automation',?) ON CONFLICT(k) DO UPDATE SET v=excluded.v", string(data))
	return err
}

func (a *App) stopProbeBatchLocked() {
	if a.mediaBatch.cancel != nil {
		a.mediaBatch.cancel()
		a.mediaBatch.cancel = nil
	}
	if a.mediaBatch.activity != "" {
		a.probeError(a.mediaBatch.activity, context.Canceled)
		a.mediaBatch.activity = ""
	}
	a.mediaBatch.running = false
	a.cancelProbeOwner("manual-batch")
}

func (a *App) mediaProbeAutomationAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodGet {
		respond(w, M{"Settings": a.probeAutomation(), "Roots": a.probeRootOptions()})
		return
	}
	if r.Method != http.MethodPut {
		fail(w, 405, "PUT required")
		return
	}
	var update struct {
		BatchRoots     *[]probeRoot
		MonitorRoots   *[]probeRoot
		MonitorEnabled *bool
	}
	if !body(w, r, &update) {
		return
	}
	if update.BatchRoots == nil && update.MonitorRoots == nil && update.MonitorEnabled == nil {
		fail(w, 400, "没有设置变更")
		return
	}
	a.mediaBatch.mu.Lock()
	defer a.mediaBatch.mu.Unlock()
	for _, roots := range []*[]probeRoot{update.BatchRoots, update.MonitorRoots} {
		if roots == nil {
			continue
		}
		if len(*roots) > 128 {
			fail(w, 400, "目录过多")
			return
		}
		valid := a.sanitizeProbeRoots(*roots)
		for _, root := range *roots {
			if !slices.Contains(valid, root) {
				fail(w, 400, "只能选择当前媒体库父目录")
				return
			}
		}
		*roots = valid
	}
	config := a.probeAutomationLocked()
	resetBatch := update.BatchRoots != nil && !slices.Equal(config.BatchRoots, *update.BatchRoots)
	resetMonitor := update.MonitorRoots != nil && !slices.Equal(config.MonitorRoots, *update.MonitorRoots)
	if resetBatch {
		config.BatchRoots = *update.BatchRoots
		config.Generation++
		config.Cursor, config.State = "", "idle"
		config.Done, config.Skipped, config.Failed = 0, 0, 0
	}
	if update.MonitorRoots != nil {
		config.MonitorRoots = *update.MonitorRoots
	}
	if update.MonitorEnabled != nil {
		resetMonitor = resetMonitor || config.MonitorEnabled != *update.MonitorEnabled
		config.MonitorEnabled = *update.MonitorEnabled
	}
	if err := a.storeProbeAutomation(config); err != nil {
		fail(w, 500, "保存设置失败")
		return
	}
	if resetBatch {
		a.stopProbeBatchLocked()
	}
	if resetMonitor || !config.MonitorEnabled {
		a.cancelProbeOwner("realtime-monitor")
	}
	respond(w, M{"Settings": config, "Roots": a.probeRootOptions()})
}

func (a *App) mediaProbeBatchAPI(w http.ResponseWriter, r *http.Request, path string) {
	w.Header().Set("Cache-Control", "no-store")
	if path == "/admin/media-info/batch/status" {
		if r.Method != http.MethodGet {
			fail(w, 405, "GET required")
			return
		}
		config := a.probeAutomation()
		waiting, active := 0, 0
		a.probes.mu.Lock()
		for _, job := range a.probes.queue {
			if job.owners["manual-batch"] {
				waiting++
			}
		}
		for _, job := range a.probes.jobs {
			if job.owners["manual-batch"] && job.cancel != nil {
				select {
				case <-job.done:
				default:
					active++
				}
			}
		}
		a.probes.mu.Unlock()
		respond(w, M{"State": config.State, "Done": config.Done, "Skipped": config.Skipped, "Failed": config.Failed,
			"Cursor": config.Cursor, "Generation": config.Generation, "Waiting": waiting, "Active": active, "Concurrency": a.probeSettings().Concurrency})
		return
	}
	if r.Method != http.MethodPost {
		fail(w, 405, "POST required")
		return
	}
	a.mediaBatch.mu.Lock()
	defer a.mediaBatch.mu.Unlock()
	config := a.probeAutomationLocked()
	switch path {
	case "/admin/media-info/batch/stop":
		a.stopProbeBatchLocked()
		if config.State == "running" {
			config.State = "stopped"
		}
		if err := a.storeProbeAutomation(config); err != nil {
			fail(w, 500, "停止进度保存失败")
			return
		}
		respond(w, M{"State": config.State})
	case "/admin/media-info/batch/start":
		if a.mediaBatch.stopped {
			fail(w, 503, "服务正在关闭")
			return
		}
		if a.mediaBatch.running {
			respond(w, M{"State": "running"})
			return
		}
		if len(config.BatchRoots) == 0 {
			fail(w, 400, "请先选择批量提取目录")
			return
		}
		config.State = "running"
		if err := a.storeProbeAutomation(config); err != nil {
			fail(w, 500, "启动进度保存失败")
			return
		}
		ctx, cancel := context.WithCancel(context.Background())
		a.mediaBatch.running, a.mediaBatch.cancel = true, cancel
		activity := a.newActivity("probe", "", "批量提取媒体信息")
		a.mediaBatch.activity = activity
		a.mediaBatch.workers.Add(1)
		go func() {
			defer a.mediaBatch.workers.Done()
			defer cancel()
			a.runProbeBatch(ctx, config.Generation, activity)
		}()
		respond(w, M{"State": "running"})
	default:
		fail(w, 404, "not found")
	}
}

func (a *App) probeBatchPage(config probeAutomationSettings) ([]Item, error) {
	if len(config.BatchRoots) == 0 {
		return nil, nil
	}
	conditions := make([]string, 0, len(config.BatchRoots))
	args := []any{config.Cursor}
	for _, root := range config.BatchRoots {
		prefix := root.Root
		if !strings.HasSuffix(prefix, string(filepath.Separator)) {
			prefix += string(filepath.Separator)
		}
		conditions = append(conditions, `(lib=? AND (path=? OR path LIKE ? ESCAPE '\'))`)
		args = append(args, root.Library, root.Root, catalogLike(prefix)+"%")
	}
	rows, err := a.db.Query("SELECT "+cols+" FROM items WHERE id>? AND kind IN ('Movie','Episode') AND url<>'' AND ("+
		strings.Join(conditions, " OR ")+") ORDER BY id LIMIT 100", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Item{}
	for rows.Next() {
		item, err := readItem(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (a *App) probeBatchJobResult(item Item, job *probeJob) (done, skipped, failed int64) {
	if job == nil || job.err != nil {
		return 0, 0, 1
	}
	if job.x.ID != item.ID {
		if err := a.saveMedia(item, job.data); err != nil {
			a.probeError(a.newActivity("probe", item.ID, a.mediaLabel(item)), err)
			return 0, 0, 1
		}
		return 1, 0, 0
	}
	if job.activity == "" {
		return 0, 1, 0
	}
	return 1, 0, 0
}

func (a *App) runProbeBatch(ctx context.Context, generation uint64, activity string) {
	var batchErr error
	complete := false
	a.changeActivity(activity, func(entry *activityEntry) { entry.State = "running" })
	defer func() {
		a.mediaBatch.mu.Lock()
		defer a.mediaBatch.mu.Unlock()
		if a.mediaBatch.activity != activity {
			return
		}
		config := a.probeAutomationLocked()
		if config.Generation == generation {
			config.State = "stopped"
			if complete {
				config.State = "complete"
			} else if batchErr != nil && !errors.Is(batchErr, context.Canceled) {
				config.State = "error"
			}
			if err := a.storeProbeAutomation(config); err != nil {
				batchErr = err
			}
		}
		if !complete {
			a.cancelProbeOwner("manual-batch")
		}
		a.mediaBatch.running, a.mediaBatch.cancel, a.mediaBatch.activity = false, nil, ""
		a.probeError(activity, batchErr)
		if complete && batchErr == nil {
			a.changeActivity(activity, func(entry *activityEntry) { entry.Progress = 100 })
		}
	}()
	for ctx.Err() == nil {
		config := a.probeAutomation()
		if config.Generation != generation || config.State != "running" {
			batchErr = context.Canceled
			return
		}
		items, err := a.probeBatchPage(config)
		if err != nil {
			batchErr = err
			return
		}
		if len(items) == 0 {
			complete = true
			return
		}
		for start := 0; start < len(items); {
			limit := a.probeSettings().Concurrency
			end := min(start+limit, len(items))
			jobs := make([]*probeJob, end-start)
			a.mediaBatch.mu.Lock()
			current := a.probeAutomationLocked()
			if ctx.Err() != nil || current.Generation != generation || a.mediaBatch.activity != activity {
				a.mediaBatch.mu.Unlock()
				batchErr = context.Canceled
				return
			}
			for i, item := range items[start:end] {
				jobs[i] = a.queueProbeWithOwner(item, "", "", false, "manual-batch")
			}
			a.mediaBatch.mu.Unlock()
			for _, job := range jobs {
				if job == nil {
					select {
					case <-ctx.Done():
						batchErr = ctx.Err()
						return
					case <-time.After(250 * time.Millisecond):
					}
					break
				}
				select {
				case <-ctx.Done():
					batchErr = ctx.Err()
					return
				case <-job.done:
				}
				done, skipped, failed := a.probeBatchJobResult(items[start], job)
				a.mediaBatch.mu.Lock()
				current = a.probeAutomationLocked()
				if ctx.Err() != nil || current.Generation != generation || a.mediaBatch.activity != activity {
					a.mediaBatch.mu.Unlock()
					batchErr = context.Canceled
					return
				}
				current.Done += done
				current.Skipped += skipped
				current.Failed += failed
				current.Cursor = items[start].ID
				err = a.storeProbeAutomation(current)
				a.mediaBatch.mu.Unlock()
				if err != nil {
					batchErr = err
					return
				}
				a.changeActivity(activity, func(entry *activityEntry) {
					entry.Done = int(current.Done + current.Skipped + current.Failed)
					entry.Current = fmt.Sprintf("完成 %d · 跳过 %d · 失败 %d", current.Done, current.Skipped, current.Failed)
				})
				start++
			}
		}
	}
	batchErr = ctx.Err()
}

func (a *App) stopMediaProbes() {
	a.mediaBatch.mu.Lock()
	a.mediaBatch.stopped = true
	config := a.probeAutomationLocked()
	a.stopProbeBatchLocked()
	if config.State == "running" {
		config.State = "stopped"
		if err := a.storeProbeAutomation(config); err != nil {
			log.Printf("media batch progress: %s", safeProbeError(err))
		}
	}
	a.mediaBatch.mu.Unlock()
	a.probes.mu.Lock()
	a.probes.stopped = true
	owners := map[string]bool{}
	for _, job := range a.probes.jobs {
		for owner := range job.owners {
			owners[owner] = true
		}
	}
	a.probes.mu.Unlock()
	for owner := range owners {
		a.cancelProbeOwner(owner)
	}
	a.mediaBatch.workers.Wait()
	a.probes.workers.Wait()
}
