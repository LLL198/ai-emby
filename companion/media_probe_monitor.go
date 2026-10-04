package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
)

func mediaPathWithin(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && (relative == "." || filepath.IsLocal(relative))
}

func probeMonitorPath(root probeRoot, event mediaFsEvent) bool {
	return root.Library == event.Library && root.Root == event.Root &&
		event.Op&uint32(fsnotify.Create|fsnotify.Write) != 0 &&
		strings.EqualFold(filepath.Ext(event.Name), ".strm") &&
		filepath.Clean(event.Name) != filepath.Clean(root.Root) && mediaPathWithin(root.Root, event.Name)
}

func probeMonitorStat(root, path string) (probeFileSignature, bool) {
	if !mediaPathWithin(root, path) {
		return probeFileSignature{}, false
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return probeFileSignature{}, false
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil || !mediaPathWithin(resolvedRoot, parent) {
		return probeFileSignature{}, false
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return probeFileSignature{}, false
	}
	return probeFileSignature{size: info.Size(), modified: info.ModTime().UnixNano()}, true
}

func probeMonitorConfigKey(config probeAutomationSettings) string {
	data, _ := json.Marshal(struct {
		Enabled bool
		Roots   []probeRoot
	}{config.MonitorEnabled, config.MonitorRoots})
	return string(data)
}

func (a *App) monitorMediaProbe(ctx context.Context) {
	events, unsubscribe := a.mediaEvents.subscribe()
	defer unsubscribe()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	config := a.probeAutomation()
	configKey := probeMonitorConfigKey(config)
	updated := time.Now()
	pending := map[string]*probeMonitorEntry{}
	completed := map[string]probeFileSignature{}
	type monitorJob struct {
		job       *probeJob
		item      Item
		signature probeFileSignature
	}
	active := map[string]monitorJob{}
	for {
		select {
		case <-ctx.Done():
			return
		case event := <-events:
			if !config.MonitorEnabled {
				continue
			}
			for _, root := range config.MonitorRoots {
				if !probeMonitorPath(root, event) {
					continue
				}
				key := root.Library + "\x00" + event.Name
				now := time.Now()
				if entry := pending[key]; entry != nil {
					entry.changed = now
				} else if len(pending) < 1024 {
					signature, _ := probeMonitorStat(root.Root, event.Name)
					pending[key] = &probeMonitorEntry{root: root, path: event.Name, signature: signature, changed: now, first: now}
				}
			}
		case now := <-ticker.C:
			if now.Sub(updated) >= time.Second {
				config = a.probeAutomation()
				updated = now
				nextKey := probeMonitorConfigKey(config)
				if configKey != nextKey {
					clear(pending)
					clear(completed)
					clear(active)
					configKey = nextKey
				}
			}
			if !config.MonitorEnabled {
				continue
			}
			for key, running := range active {
				select {
				case <-running.job.done:
					delete(active, key)
					if running.job.err == nil {
						_, _, failed := a.probeBatchJobResult(running.item, running.job)
						if failed == 0 {
							if len(completed) >= 4096 {
								clear(completed)
							}
							completed[key] = running.signature
						}
					}
				default:
				}
			}
			processed := 0
			for key, entry := range pending {
				if now.Sub(entry.first) > 120*time.Second {
					delete(pending, key)
					continue
				}
				if processed >= 16 || len(active) >= 1024 || active[key].job != nil || now.Before(entry.retry) {
					continue
				}
				processed++
				entry.retry = now.Add(2 * time.Second)
				signature, ok := probeMonitorStat(entry.root.Root, entry.path)
				if !ok {
					continue
				}
				if signature != entry.signature {
					entry.signature, entry.changed = signature, now
					continue
				}
				if now.Sub(entry.changed) < 2*time.Second {
					continue
				}
				if previous, exists := completed[key]; exists && previous == signature {
					delete(pending, key)
					continue
				}
				item, err := readItem(a.db.QueryRow("SELECT "+cols+" FROM items WHERE lib=? AND path=? AND kind IN (?,?)",
					entry.root.Library, entry.path, "Movie", "Episode"))
				if err != nil || item.URL == "" {
					continue
				}
				a.mediaBatch.mu.Lock()
				current := a.probeAutomationLocked()
				var job *probeJob
				if ctx.Err() == nil && probeMonitorConfigKey(current) == configKey {
					job = a.queueProbeWithOwner(item, "", "", false, "realtime-monitor")
				}
				a.mediaBatch.mu.Unlock()
				if job != nil {
					active[key] = monitorJob{job: job, item: item, signature: signature}
					delete(pending, key)
				}
			}
		}
	}
}
