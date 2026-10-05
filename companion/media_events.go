package main

import (
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type mediaFsEvent struct {
	Name    string
	Op      uint32
	Library string
	Root    string
}

type mediaEventHub struct {
	mu   sync.RWMutex
	next uint64
	subs map[uint64]chan mediaFsEvent
}

type mediaRefreshGuard struct {
	mu             sync.Mutex
	scraped        map[string]time.Time
	pending        map[string]map[string]bool
	timers         map[string]*time.Timer
	generation     map[string]uint64
	nextGeneration uint64
	manual         map[string]map[string]bool
}

// Unsubscribing removes the entry without closing the event channel.
func (hub *mediaEventHub) subscribe() (chan mediaFsEvent, func()) {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	if hub.subs == nil {
		hub.subs = make(map[uint64]chan mediaFsEvent)
	}
	hub.next++
	key := hub.next
	events := make(chan mediaFsEvent, 4096)
	hub.subs[key] = events
	return events, func() {
		hub.mu.Lock()
		delete(hub.subs, key)
		hub.mu.Unlock()
	}
}

// Drop events for full subscribers so one slow consumer cannot block publication.
func (hub *mediaEventHub) publish(event mediaFsEvent) {
	hub.mu.RLock()
	defer hub.mu.RUnlock()
	for _, events := range hub.subs {
		select {
		case events <- event:
		default:
		}
	}
}

func refreshPathsOverlap(first, second string) bool {
	first, second = filepath.Clean(first), filepath.Clean(second)
	return first == second || strings.HasPrefix(first, second+"/") || strings.HasPrefix(second, first+"/")
}

func compactRefreshPaths(paths []string) []string {
	unique := make(map[string]bool)
	for _, path := range paths {
		if path != "" {
			unique[filepath.Clean(path)] = true
		}
	}
	ordered := make([]string, 0, len(unique))
	for path := range unique {
		ordered = append(ordered, path)
	}
	sort.Strings(ordered)
	result := make([]string, 0, len(ordered))
	for _, path := range ordered {
		covered := false
		for _, parent := range result {
			if path == parent || strings.HasPrefix(path, parent+"/") {
				covered = true
				break
			}
		}
		if !covered {
			result = append(result, path)
		}
	}
	return result
}

func (a *App) takeQueuedRefresh(libraryID string, paths []string) []string {
	a.refreshGuard.mu.Lock()
	defer a.refreshGuard.mu.Unlock()
	result := append([]string(nil), paths...)
	for queued := range a.refreshGuard.pending[libraryID] {
		for _, path := range paths {
			if refreshPathsOverlap(path, queued) {
				result = append(result, queued)
				delete(a.refreshGuard.pending[libraryID], queued)
				delete(a.refreshGuard.manual[libraryID], queued)
				break
			}
		}
	}
	return compactRefreshPaths(result)
}

func (a *App) markScraperRefresh(libraryID string, paths []string) {
	now := time.Now()
	until := now.Add(time.Duration(a.watchDelay()+30) * time.Second)
	a.refreshGuard.mu.Lock()
	defer a.refreshGuard.mu.Unlock()
	if a.refreshGuard.scraped == nil {
		a.refreshGuard.scraped = make(map[string]time.Time)
	}
	for key, deadline := range a.refreshGuard.scraped {
		if now.After(deadline) {
			delete(a.refreshGuard.scraped, key)
		}
	}
	for _, path := range paths {
		if path != "" {
			a.refreshGuard.scraped[libraryID+"\x00"+filepath.Clean(path)] = until
		}
	}
}

func (a *App) automaticRefreshOwner() string {
	if a.scraperSettings().MonitorAutoRefresh {
		return "刮削管理"
	}
	if a.defaultOn("watch_enabled") {
		return "增强管理"
	}
	return ""
}

// Ignore stale callbacks and defer refreshes while the scraper is active.
func (a *App) queueBatchRefresh(libraryID string, paths []string, manual bool) {
	if libraryID == "" {
		return
	}
	delay := time.Duration(a.watchDelay()) * time.Second
	if delay < 5*time.Second {
		delay = 5 * time.Second
	}
	paths = compactRefreshPaths(paths)
	if len(paths) == 0 {
		return
	}
	guard := &a.refreshGuard
	guard.mu.Lock()
	if guard.pending == nil {
		guard.pending = make(map[string]map[string]bool)
	}
	if guard.timers == nil {
		guard.timers = make(map[string]*time.Timer)
	}
	if guard.generation == nil {
		guard.generation = make(map[string]uint64)
	}
	if guard.pending[libraryID] == nil {
		guard.pending[libraryID] = make(map[string]bool)
	}
	for _, path := range paths {
		guard.pending[libraryID][filepath.Clean(path)] = true
	}
	if manual {
		if guard.manual == nil {
			guard.manual = make(map[string]map[string]bool)
		}
		if guard.manual[libraryID] == nil {
			guard.manual[libraryID] = make(map[string]bool)
		}
		for _, path := range paths {
			guard.manual[libraryID][path] = true
		}
	}
	guard.nextGeneration++
	generation := guard.nextGeneration
	guard.generation[libraryID] = generation
	if timer := guard.timers[libraryID]; timer != nil {
		timer.Stop()
	}
	var callback func()
	callback = func() {
		a.scraper.mu.Lock()
		watchDelay := a.watchDelay()
		now := time.Now()
		guard.mu.Lock()
		if guard.generation[libraryID] != generation {
			guard.mu.Unlock()
			a.scraper.mu.Unlock()
			return
		}
		owner := a.automaticRefreshOwner()
		if a.scraper.running {
			guard.timers[libraryID] = time.AfterFunc(delay, callback)
			guard.mu.Unlock()
			a.scraper.mu.Unlock()
			return
		}
		var selected []string
		for path := range guard.pending[libraryID] {
			if owner != "" || guard.manual[libraryID][path] {
				selected = append(selected, path)
			}
		}
		selected = compactRefreshPaths(selected)
		delete(guard.pending, libraryID)
		delete(guard.manual, libraryID)
		delete(guard.timers, libraryID)
		delete(guard.generation, libraryID)
		if guard.scraped == nil {
			guard.scraped = make(map[string]time.Time)
		}
		until := now.Add(time.Duration(watchDelay+30) * time.Second)
		for _, path := range selected {
			guard.scraped[libraryID+"\x00"+filepath.Clean(path)] = until
		}
		guard.mu.Unlock()
		a.scraper.mu.Unlock()
		if len(selected) != 0 {
			a.refreshMediaPaths(libraryID, selected)
			a.scraperPhase("合并刷新完成", scraperScope{Library: libraryID}, MediaRecognition{}, "", owner+"合并队列：写入稳定，已刷新本地元数据索引")
		}
	}
	guard.timers[libraryID] = time.AfterFunc(delay, callback)
	guard.mu.Unlock()
}

func (a *App) cancelScraperRefreshQueue() {
	guard := &a.refreshGuard
	guard.mu.Lock()
	defer guard.mu.Unlock()
	for libraryID, timer := range guard.timers {
		manual := guard.manual[libraryID]
		if len(manual) == 0 {
			if timer != nil {
				timer.Stop()
			}
			delete(guard.pending, libraryID)
			delete(guard.timers, libraryID)
			delete(guard.generation, libraryID)
		} else {
			pending := make(map[string]bool)
			for path := range manual {
				pending[path] = true
			}
			guard.pending[libraryID] = pending
		}
	}
}
