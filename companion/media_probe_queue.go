package main

import (
	"context"
	"errors"
	"net/http"
	"regexp"
)

var probeErrorURL = regexp.MustCompile(`(?i)https?://[^\s<>"']+`)

func safeProbeError(err error) string {
	if err == nil {
		return ""
	}
	return probeErrorURL.ReplaceAllString(err.Error(), "[URL 已脱敏]")
}

func (a *App) probeError(activity string, err error) {
	a.changeActivity(activity, func(entry *activityEntry) {
		entry.State, entry.Error = "complete", ""
		if errors.Is(err, context.Canceled) {
			entry.State = "cancelled"
		} else if err != nil {
			entry.State, entry.Error = "error", safeProbeError(err)
		}
	})
}

func (a *App) mediaProbeStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		fail(w, 405, "GET required")
		return
	}
	config := a.probeSettings()
	a.probes.mu.Lock()
	waiting, running := len(a.probes.queue), a.probes.running
	a.probes.mu.Unlock()
	w.Header().Set("Cache-Control", "no-store")
	respond(w, M{"ProbeWaiting": waiting, "ProbeRunning": running, "ProbeConcurrency": config.Concurrency})
}

func (a *App) queueProbeRequest(item Item, r *http.Request, force bool, automatic ...bool) *probeJob {
	return a.queueProbeContext(item, token(r), r.UserAgent(), force, automatic...)
}

func (a *App) queueProbeContext(item Item, token, ua string, force bool, automatic ...bool) *probeJob {
	owner := "request"
	if len(automatic) > 0 && automatic[0] {
		owner = "next-episode"
	} else if len(automatic) > 1 && automatic[1] {
		owner = "browse"
	}
	return a.queueProbeWithOwner(item, token, ua, force, owner, automatic...)
}

func (a *App) queueProbeWithOwner(item Item, token, ua string, force bool, owner string, automatic ...bool) *probeJob {
	if item.URL == "" {
		return nil
	}
	if !force {
		if cached := a.cachedMedia(item); len(cached) > 0 && cached["Partial"] != true {
			job := &probeJob{x: item, done: make(chan struct{}), data: cached, owners: map[string]bool{owner: true}}
			close(job.done)
			return job
		}
	}
	a.probes.mu.Lock()
	defer a.probes.mu.Unlock()
	if a.probes.stopped {
		return nil
	}
	config := a.probeSettings()
	if (owner == "browse" && !config.Browse) || (owner == "next-episode" && !config.PreloadNext) {
		return nil
	}
	if a.probes.jobs == nil {
		a.probes.jobs = map[string]*probeJob{}
	}
	key := digest(item.URL)
	if job := a.probes.jobs[key]; job != nil && !job.canceledOwner {
		if job.owners == nil {
			job.owners = map[string]bool{}
		}
		job.owners[owner] = true
		return job
	}
	if len(a.probes.queue) >= 1000 {
		return nil
	}
	job := &probeJob{x: item, token: token, ua: ua, activity: a.newActivity("probe", item.ID, a.mediaLabel(item)), done: make(chan struct{}), owners: map[string]bool{owner: true}, force: force}
	job.autoNext = len(automatic) > 0 && automatic[0]
	job.autoBrowse = len(automatic) > 1 && automatic[1]
	a.probes.jobs[key] = job
	a.probes.queue = append(a.probes.queue, job)
	a.dispatchProbesLocked(config.Concurrency)
	return job
}

func (a *App) cancelProbeOwner(owner string) {
	a.probes.mu.Lock()
	defer a.probes.mu.Unlock()
	queue := a.probes.queue[:0]
	for _, job := range a.probes.queue {
		delete(job.owners, owner)
		if len(job.owners) > 0 {
			queue = append(queue, job)
			continue
		}
		job.canceledOwner, job.err = true, context.Canceled
		if key := digest(job.x.URL); a.probes.jobs[key] == job {
			delete(a.probes.jobs, key)
		}
		a.probeError(job.activity, context.Canceled)
		close(job.done)
	}
	clear(a.probes.queue[len(queue):])
	a.probes.queue = queue
	if len(queue) == 0 {
		a.probes.queue = nil
	}
	for key, job := range a.probes.jobs {
		if !job.owners[owner] {
			continue
		}
		delete(job.owners, owner)
		if len(job.owners) == 0 && job.cancel != nil {
			job.canceledOwner = true
			job.cancel()
			delete(a.probes.jobs, key)
		}
	}
}

func writeMediaFile(directory string, record mediaRecord) error {
	return atomicMediaFile(directory, record)
}

func (a *App) queueBrowseMediaInfo(w http.ResponseWriter, r *http.Request, item Item) {
	_ = http.NewResponseController(w).Flush()
	if a.probeSettings().Browse {
		a.queueProbeRequest(item, r, false, false, true)
	}
}
