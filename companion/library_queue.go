package main

import (
	"strconv"
	"sync"
)

type libraryJob struct {
	lib    string
	update bool
	ready  chan struct{}
}
type libraryJobQueue struct {
	mu      sync.Mutex
	pending []*libraryJob
	active  map[string]bool
	running [2]int
}

func (a *App) jobLimit(update bool) int {
	key := "scan_concurrency"
	if update {
		key = "update_concurrency"
	}
	var v string
	a.db.QueryRow("SELECT v FROM settings WHERE k=?", key).Scan(&v)
	n, e := strconv.Atoi(v)
	if e != nil || n < 1 || n > 64 {
		return 2
	}
	return n
}
func (a *App) displayName() string {
	var v string
	a.db.QueryRow("SELECT v FROM settings WHERE k='server_name'").Scan(&v)
	if v == "" {
		return "go-emby"
	}
	return v
}
func (a *App) maskedKey(key string) string {
	var v string
	a.db.QueryRow("SELECT v FROM settings WHERE k=?", "key-mask:"+key).Scan(&v)
	if v == "" {
		return "••••••••••••（历史密钥）"
	}
	return v
}

// Called with the queue lock held. Pending jobs live only in process memory.
func (a *App) dispatchLibraryJobs() {
	q := &a.jobs
	if q.active == nil {
		q.active = map[string]bool{}
	}
	limits := [2]int{a.jobLimit(false), a.jobLimit(true)}
	blocked := map[string]bool{}
	for i := 0; i < len(q.pending); {
		j := q.pending[i]
		k := 0
		if j.update {
			k = 1
		}
		if q.running[k] >= limits[k] || q.active[j.lib] || blocked[j.lib] {
			blocked[j.lib] = true
			i++
			continue
		}
		q.pending = append(q.pending[:i], q.pending[i+1:]...)
		q.active[j.lib] = true
		q.running[k]++
		close(j.ready)
	}
}
func (a *App) acquireLibraryJob(lib string, update bool) func() {
	q := &a.jobs
	j := &libraryJob{lib: lib, update: update, ready: make(chan struct{})}
	q.mu.Lock()
	q.pending = append(q.pending, j)
	a.dispatchLibraryJobs()
	q.mu.Unlock()
	<-j.ready
	return func() {
		q.mu.Lock()
		delete(q.active, lib)
		k := 0
		if update {
			k = 1
		}
		q.running[k]--
		a.dispatchLibraryJobs()
		q.mu.Unlock()
	}
}
func (a *App) wakeLibraryJobs() { a.jobs.mu.Lock(); defer a.jobs.mu.Unlock(); a.dispatchLibraryJobs() }
