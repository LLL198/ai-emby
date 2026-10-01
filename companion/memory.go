package main

import (
	"runtime"
	"runtime/debug"
	"time"
)

// Go's scavenger handles normal reclamation. Return large idle arenas after
// bursts only when no playback or metadata work is active; never on a request.
func (a *App) reclaimIdleMemory() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		a.probes.mu.Lock()
		busy := a.probes.running > 0 || len(a.probes.queue) > 0
		a.probes.mu.Unlock()
		if busy {
			continue
		}
		var active int
		if a.db.QueryRow("SELECT count(*) FROM plays WHERE updated>?", time.Now().Unix()-180).Scan(&active) != nil || active > 0 {
			continue
		}
		var stats runtime.MemStats
		runtime.ReadMemStats(&stats)
		if stats.HeapIdle > stats.HeapReleased && stats.HeapIdle-stats.HeapReleased >= 64<<20 {
			debug.FreeOSMemory()
		}
	}
}
