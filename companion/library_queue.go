package main

import (
	"errors"
	"strconv"
	"strings"
	"sync"
)

type libraryJob struct {
	lib    string
	update bool
	ready  chan struct{}
}
type libraryJobQueue struct {
	mu        sync.Mutex
	pending   []*libraryJob
	active    map[string]bool
	running   [2]int
	requested map[string]bool
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
		return "AI Emby"
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

func (a *App) requestLibraryScans(libraries []string, update, scheduled bool) bool {
	a.jobs.mu.Lock()
	a.scanner.mu.Lock()
	modern := a.scanner.locks != nil
	busy := len(a.jobs.requested) > 0 || len(a.jobs.pending) > 0 || len(a.jobs.active) > 0 || a.jobs.running[0] > 0 || a.jobs.running[1] > 0 || len(a.scanner.active) > 0
	if scheduled && busy {
		a.scanner.mu.Unlock()
		a.jobs.mu.Unlock()
		activity := a.newActivity("scan", "", "定时扫描")
		a.changeActivity(activity, func(entry *activityEntry) {
			entry.State, entry.Current = "complete", "跳过：已有扫描运行或排队"
		})
		return false
	}
	if a.jobs.requested == nil {
		a.jobs.requested = map[string]bool{}
	}
	selected := []string{}
	for _, library := range libraries {
		library = strings.TrimSpace(library)
		if library == "" || a.jobs.requested[library] || a.jobs.active[library] {
			continue
		}
		pending := false
		for _, job := range a.jobs.pending {
			if job.lib == library {
				pending = true
				break
			}
		}
		if pending {
			continue
		}
		if modern {
			if _, ok := a.reserveConcurrentScanLocked(library); !ok {
				continue
			}
		}
		a.jobs.requested[library] = true
		selected = append(selected, library)
	}
	a.scanner.mu.Unlock()
	a.jobs.mu.Unlock()
	if len(selected) == 0 {
		return false
	}
	activity := ""
	if scheduled {
		activity = a.newActivity("scan", "", "定时扫描")
		a.changeActivity(activity, func(entry *activityEntry) {
			entry.State, entry.Current, entry.Total = "running", "开始", len(selected)
		})
	}
	jobs := make(chan string, len(selected))
	for _, library := range selected {
		jobs <- library
	}
	close(jobs)
	var workers sync.WaitGroup
	var progress sync.Mutex
	done := 0
	failures := []error{}
	workerCount := min(len(selected), a.jobLimit(update))
	for worker := 0; worker < workerCount; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for library := range jobs {
				var err error
				if modern {
					err = a.runConcurrentScanResult(a.scanner.ctx, library, update, false, nil)
				} else {
					a.scanLibraryScopedLegacy(library, update, false, nil)
					var state, message string
					err = a.db.QueryRow("SELECT status,error FROM libraries WHERE id=?", library).Scan(&state, &message)
					if err == nil && (state == "error" || message != "") {
						err = errors.New(message)
					}
				}
				a.jobs.mu.Lock()
				delete(a.jobs.requested, library)
				a.jobs.mu.Unlock()
				if activity != "" {
					progress.Lock()
					done++
					if err != nil {
						failures = append(failures, err)
					}
					a.changeActivity(activity, func(entry *activityEntry) {
						entry.Done, entry.Current = done, library
					})
					progress.Unlock()
				}
			}
		}()
	}
	if activity != "" {
		go func() {
			workers.Wait()
			a.finishActivity(activity, errors.Join(failures...))
		}()
	}
	return true
}
