package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
)

// scraperDirectorySnapshot returns safe media entries and a stable digest of
// their names and file metadata for monitor change detection.
func scraperDirectorySnapshot(rootPath, relative string) ([]fs.DirEntry, string, error) {
	root, err := scraperScopePath(rootPath, relative)
	if err != nil {
		return nil, "", err
	}
	defer root.Close()

	directory, err := root.OpenFile(".", os.O_RDONLY, 0)
	if err != nil {
		return nil, "", err
	}
	defer directory.Close()

	entries, err := directory.ReadDir(-1)
	if err != nil {
		return nil, "", err
	}
	mediaEntries := make([]fs.DirEntry, 0)
	signatures := make([]string, 0)
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		isDirectory := entry.IsDir()
		if !isDirectory && !scraperMediaFile(entry.Name()) {
			continue
		}

		info, err := root.Lstat(entry.Name())
		if err != nil {
			return nil, "", err
		}
		if isDirectory {
			signatures = append(signatures, "d:"+entry.Name())
		} else {
			signatures = append(signatures, fmt.Sprintf("%s:%d:%d", entry.Name(), info.Size(), info.ModTime().UnixNano()))
		}
		mediaEntries = append(mediaEntries, entry)
	}

	sort.Strings(signatures)
	return mediaEntries, digest(strings.Join(signatures, "\x00")), nil
}

// scraperMonitorRoots expands each library into its configured roots and
// orders them by the concatenated library ID and root path.
func (a *App) scraperMonitorRoots() []scraperMonitorRoot {
	var roots []scraperMonitorRoot
	for _, library := range a.libraries() {
		locations := library["Locations"].([]string)
		libraryID := library["Id"].(string)
		kind := library["CollectionType"].(string)
		for _, root := range locations {
			roots = append(roots, scraperMonitorRoot{
				Library: libraryID,
				Root:    root,
				Kind:    kind,
			})
		}
	}
	sort.Slice(roots, func(i, j int) bool {
		return roots[i].Library+roots[i].Root < roots[j].Library+roots[j].Root
	})
	return roots
}

// Require an unchanged directory signature for two seconds.
func (discovery *scraperDiscovery) stable(signature string, now time.Time) bool {
	if discovery.signature != signature {
		discovery.signature = signature
		discovery.stableSince = now
		return false
	}
	return now.Sub(discovery.stableSince) >= 2*time.Second
}

// Monitor stable directories, running one automatic task while manual tasks are idle.
func (a *App) monitorScraper(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	events, unsubscribe := a.mediaEvents.subscribe()
	defer unsubscribe()

	discoveries := make(map[string]*scraperDiscovery)
	completed := make(map[string]string)
	results := make(chan scraperMonitorResult, 1)
	var cancel context.CancelFunc
	active := false
	defer func() {
		if cancel != nil {
			cancel()
		}
		if active {
			<-results
		}
	}()

	var config scraperConfig
	var roots []scraperMonitorRoot
	configKey := ""
	enabled := false

	discover := func(root scraperMonitorRoot, path string) {
		relative, err := filepath.Rel(root.Root, path)
		if err != nil || !filepath.IsLocal(relative) {
			return
		}
		if !scraperScopeEnabled(config.MonitorScopes, root.Library, root.Root, relative, false) {
			return
		}
		key := root.Library + "\x00" + root.Root + "\x00" + relative
		if _, exists := discoveries[key]; exists {
			return
		}
		scope := scraperScope{Library: root.Library, Root: root.Root, Relative: relative}
		if len(discoveries) >= 1024 {
			a.scraperPhase("跳过", scope, MediaRecognition{}, config.Scraper, "监控队列已满")
			return
		}
		discoveries[key] = &scraperDiscovery{
			scope:       scope,
			libraryType: root.Kind,
			discovered:  time.Now(),
		}
		a.scraperPhase("发现目录", scope, MediaRecognition{}, config.Scraper, "收到系统文件事件，等待媒体文件稳定")
	}

	for {
		select {
		case <-ctx.Done():
			return
		case event := <-events:
			if !enabled || a.scraperIgnore(event.Name) ||
				event.Op&uint32(fsnotify.Remove|fsnotify.Rename) != 0 ||
				event.Op&uint32(fsnotify.Create|fsnotify.Write) == 0 {
				continue
			}
			var root scraperMonitorRoot
			found := false
			for _, candidate := range roots {
				if candidate.Library == event.Library && candidate.Root == event.Root {
					root, found = candidate, true
					break
				}
			}
			if !found {
				continue
			}
			info, err := os.Lstat(event.Name)
			if err != nil || info.Mode()&os.ModeSymlink != 0 {
				continue
			}
			if !info.IsDir() {
				if scraperMediaFile(event.Name) {
					discover(root, filepath.Dir(event.Name))
				}
				continue
			}
			discover(root, event.Name)
			parent := filepath.Dir(event.Name)
			if parent == root.Root || strings.HasPrefix(parent, root.Root+"/") {
				discover(root, parent)
			}

		case result := <-results:
			active = false
			cancel = nil
			discovery := discoveries[result.key]
			if discovery == nil || result.configKey != configKey {
				continue
			}
			discovery.busy = false
			discovery.failedAttempts = result.failedAttempts
			retrySeconds := 5 << discovery.failedAttempts
			if retrySeconds > 120 {
				retrySeconds = 120
			}
			discovery.nextAttempt = time.Now().Add(time.Duration(retrySeconds) * time.Second)
			if !result.retry {
				if len(completed) >= 1024 {
					completed = make(map[string]string)
				}
				completed[result.key] = result.signature
				delete(discoveries, result.key)
			}

		case now := <-ticker.C:
			nextConfig := a.scraperSettings()
			nextRoots := a.scraperMonitorRoots()
			payload, _ := json.Marshal(struct {
				Config scraperConfig
				Roots  []scraperMonitorRoot
			}{nextConfig, nextRoots})
			nextKey := string(payload)
			if nextKey != configKey {
				if cancel != nil {
					cancel()
				}
				config, roots, configKey = nextConfig, nextRoots, nextKey
				enabled = config.Enabled && config.MonitorEnabled
				for key, discovery := range discoveries {
					rootPresent := false
					for _, root := range roots {
						if root.Library == discovery.scope.Library && root.Root == discovery.scope.Root && root.Kind == discovery.libraryType {
							rootPresent = true
							break
						}
					}
					if !enabled || !rootPresent || !scraperScopeEnabled(config.MonitorScopes,
						discovery.scope.Library, discovery.scope.Root, discovery.scope.Relative, false) {
						delete(discoveries, key)
						continue
					}
					discovery.busy = false
					discovery.nextAttempt = time.Time{}
				}
				completed = make(map[string]string)
				if enabled {
					a.scraperPhase("实时监控开启", scraperScope{}, MediaRecognition{}, config.Scraper,
						"复用唯一系统事件监听；是否合并刷新由独立自动刷新开关控制")
				} else {
					a.scraperPhase("实时监控关闭", scraperScope{}, MediaRecognition{}, config.Scraper,
						"实时监控或刮削总开关关闭，自动任务跳过")
				}
			}
			if !enabled || active {
				continue
			}
			for key, discovery := range discoveries {
				if now.Sub(discovery.discovered) > 10*time.Minute {
					a.scraperPhase("跳过", discovery.scope, MediaRecognition{}, config.Scraper,
						"等待稳定或媒体索引超时；请更新媒体库后手动刮削")
					delete(discoveries, key)
					continue
				}
				entries, signature, err := scraperDirectorySnapshot(discovery.scope.Root, discovery.scope.Relative)
				if err != nil {
					delete(discoveries, key)
					continue
				}
				if discovery.busy || now.Before(discovery.nextAttempt) || !discovery.stable(signature, now) {
					continue
				}
				if completed[key] == signature {
					delete(discoveries, key)
					continue
				}
				a.scraper.mu.Lock()
				blocked := a.scraper.running || a.scraper.planning || a.scraper.plan != nil
				a.scraper.mu.Unlock()
				if blocked {
					continue
				}
				a.scraperPhase("目录稳定", discovery.scope, MediaRecognition{}, config.Scraper,
					"系统事件目录签名连续稳定")
				discovery.busy, active = true, true
				var workerCtx context.Context
				workerCtx, cancel = context.WithCancel(ctx)
				copyDiscovery := *discovery
				workerCancel := cancel
				go func(key, signature, configKey string, config scraperConfig) {
					retry := a.scraperAutoDirectory(workerCtx, &copyDiscovery, entries, config)
					workerCancel()
					results <- scraperMonitorResult{
						key: key, signature: signature, configKey: configKey,
						failedAttempts: copyDiscovery.failedAttempts, retry: retry,
					}
				}(key, signature, configKey, config)
				break
			}
		}
	}
}

// scraperRecognizeDirectory recognizes one directory using its own entry
// snapshot and, when available, a recognition of its parent directory.
func (a *App) scraperRecognizeDirectory(entries []fs.DirEntry, libraryID, root, relative, libraryType string) MediaRecognition {
	directory := filepath.Join(root, relative)

	var parent *MediaRecognition
	if relative != "." {
		parentEntries, _, err := scraperDirectorySnapshot(root, filepath.Dir(relative))
		if err == nil {
			parentDirectory := filepath.Dir(directory)
			parentRecognition := recognizeMedia(MediaRecognitionInput{
				Library:     libraryID,
				LibraryType: libraryType,
				Directory:   parentDirectory,
				Name:        filepath.Base(parentDirectory),
				Entries:     parentEntries,
			})
			parent = &parentRecognition
		}
	}

	return recognizeMedia(MediaRecognitionInput{
		Library:     libraryID,
		LibraryType: libraryType,
		Directory:   directory,
		Name:        filepath.Base(directory),
		Entries:     entries,
		Parent:      parent,
	})
}

// Return whether the monitor should retry this directory.
func (a *App) scraperAutoDirectory(ctx context.Context, discovery *scraperDiscovery, entries []fs.DirEntry, config scraperConfig) bool {
	scope := discovery.scope
	a.scraperPhase("识别中", scope, MediaRecognition{}, config.Scraper, "规则识别器")
	recognition := a.scraperRecognizeDirectory(entries, scope.Library, scope.Root, scope.Relative, discovery.libraryType)
	if !recognition.Matched || recognition.Kind == "Unknown" {
		a.scraperPhase("未知跳过", scope, recognition, config.Scraper, recognition.Reason)
		return false
	}
	a.scraperPhase("识别类型", scope, recognition, config.Scraper, recognition.Reason)

	directory := filepath.Join(scope.Root, scope.Relative)
	rows, err := a.db.QueryContext(ctx,
		"SELECT "+cols+" FROM items WHERE lib=$1 AND (path=$2 OR substr(path,1,length($3::text))=$3) ORDER BY path,kind",
		scope.Library, directory, directory+"/")
	if err != nil {
		a.scraperPhase("等待索引", scope, recognition, config.Scraper, "读取索引失败："+a.scraperSafeError(err).Error())
		return true
	}
	var items []Item
	indexed := make(map[string]bool)
	for rows.Next() {
		item, err := readItem(rows)
		if err != nil {
			_ = rows.Close()
			return true
		}
		if item.Path == directory || filepath.Dir(item.Path) == directory {
			items = append(items, item)
			indexed[item.Path] = true
		}
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return true
	}
	for _, entry := range entries {
		if !entry.IsDir() && strings.EqualFold(filepath.Ext(entry.Name()), ".strm") &&
			!indexed[filepath.Join(directory, entry.Name())] {
			a.scraperPhase("等待索引", scope, recognition, config.Scraper, "发现早于普通媒体库更新，稍后重试")
			return true
		}
	}
	if len(items) == 0 {
		return true
	}

	a.scraper.mu.Lock()
	current := a.scraperSettings()
	if !current.Enabled || !current.MonitorEnabled ||
		!scraperScopeEnabled(current.MonitorScopes, scope.Library, scope.Root, scope.Relative, false) || ctx.Err() != nil {
		a.scraper.mu.Unlock()
		a.scraperPhase("跳过", scope, recognition, current.Scraper, "开关或监控范围已改变")
		return false
	}
	if a.scraper.running || a.scraper.planning || a.scraper.plan != nil {
		a.scraper.mu.Unlock()
		return true
	}
	a.scraper.running = true
	a.scraper.automatic = true
	workerCtx, cancel := context.WithCancel(ctx)
	a.scraper.cancel = cancel
	a.scraper.mu.Unlock()

	current.ManualScopes = nil
	activityID := a.newActivity("scraper", "", "实时监控 · 自动刮削计划")
	plan, err := a.buildScraperItems(workerCtx, current, items, activityID)
	a.finishActivity(activityID, err)
	if err != nil {
		a.scraper.mu.Lock()
		a.scraper.running = false
		a.scraper.automatic = false
		a.scraper.cancel = nil
		a.scraper.mu.Unlock()
		cancel()
		return false
	}
	defer cancel()

	for index := range plan.Objects {
		object := &plan.Objects[index]
		objectRecognition := recognition
		if object.Kind == "Episode" {
			objectRecognition = recognizeMedia(MediaRecognitionInput{
				Library: scope.Library, LibraryType: discovery.libraryType,
				Directory: directory, Name: filepath.Base(object.Item.Path), Parent: &recognition,
			})
		}
		if !objectRecognition.Matched || objectRecognition.Kind != object.Kind ||
			(object.Kind == "Episode" && (objectRecognition.Season != object.Item.Season || objectRecognition.Episode != object.Item.Episode)) {
			object.Disabled = true
			a.scraperPhase("跳过", scope, objectRecognition, current.Scraper, "识别结果与媒体索引不一致")
			continue
		}
		object.Recognition = &objectRecognition
	}
	for _, object := range plan.Objects {
		if object.Disabled {
			continue
		}
		for _, target := range object.Targets {
			action := map[string]string{"create": "新建", "skip": "跳过", "overwrite": "覆盖"}[target.Action]
			if action == "" {
				action = target.Action
			}
			a.scraperPhase("计划", scope, recognition, current.Scraper,
				object.Kind+" · "+target.Content+" · "+action+" · 文件："+target.Path)
		}
	}
	a.scraperPhase("已排队", scope, recognition, current.Scraper, "实时监控使用最新刮削内容设置；目录范围仅使用实时监控目录")
	a.scraperPhase("开始", scope, recognition, current.Scraper, "实时监控自动刮削")
	err = a.runScraper(workerCtx, plan)
	if err == nil {
		_, signature, snapshotErr := scraperDirectorySnapshot(scope.Root, scope.Relative)
		if snapshotErr == nil && signature != discovery.signature {
			return true
		}
		if workerCtx.Err() == nil {
			a.scraperPhase("成功", scope, recognition, current.Scraper, "逐媒体结果见刮削任务日志")
		} else {
			a.scraperPhase("失败", scope, recognition, current.Scraper, "自动刮削已取消")
		}
		return false
	}
	discovery.failedAttempts++
	retry := workerCtx.Err() == nil && discovery.failedAttempts < 5
	reason := a.scraperSafeError(err).Error()
	if retry {
		reason += "；自动退避重试"
	} else {
		reason += "；自动重试已停止"
	}
	a.scraperPhase("失败", scope, recognition, current.Scraper, reason)
	return retry
}
