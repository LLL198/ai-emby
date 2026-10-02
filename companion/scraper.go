package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	maxScraperErrorBytes = 1024
	maxScraperErrorRunes = 256
)

func scraperAllowedTarget(target scraperTarget, item Item) bool {
	if strings.EqualFold(filepath.Ext(target.Path), ".webp") {
		return false
	}
	if (item.Kind == "Movie" || item.Kind == "Episode") &&
		!featureMediaExtension(item.Path) {
		return false
	}
	for _, content := range scraperCategoryContents[item.Kind] {
		if content != target.Content {
			continue
		}
		for _, path := range scraperCandidates(content, item.Kind, item.Path, item.Season) {
			if path == target.Path && path != item.Path {
				return true
			}
		}
	}
	return false
}

// Manual tasks remain active until completion or explicit cancellation.
func (a *App) scraperAdmin(w http.ResponseWriter, r *http.Request, path string) {
	switch path {
	case "/admin/scraper":
		switch r.Method {
		case http.MethodGet:
			a.scraper.mu.Lock()
			running, planning, paused := a.scraper.running, a.scraper.planning, a.scraper.pause != nil
			a.scraper.mu.Unlock()
			scraperRegistryMu.RLock()
			names := make([]string, 0, len(scraperRegistry))
			for name := range scraperRegistry {
				names = append(names, name)
			}
			scraperRegistryMu.RUnlock()
			respond(w, M{"Settings": a.scraperSettings(), "Scrapers": names, "Running": running, "Planning": planning, "Paused": paused})
		case http.MethodPut:
			config := scraperConfig{Scraper: "TMDB", ChineseMetadata: true, MonitorAutoRefresh: true, Categories: make(map[string]scraperCategory)}
			for kind, content := range scraperCategoryContents {
				config.Categories[kind] = scraperCategory{Enabled: true, Content: append([]string(nil), content[:2]...)}
			}
			if !body(w, r, &config) {
				return
			}
			if config.Concurrency < 0 || config.Concurrency > maxScraperConcurrency {
				fail(w, http.StatusBadRequest, "并发数必须是 1–32 的整数")
				return
			}
			config.Concurrency = scraperConcurrency(config.Concurrency)
			config.ManualScopes = a.sanitizeScraperScopes(config.ManualScopes)
			config.MonitorScopes = a.sanitizeMonitorScopes(config.MonitorScopes)
			if !validScraperConfig(config) {
				fail(w, http.StatusBadRequest, "刮削设置有误：至少选择一个有效且不重复的刮削器，并检查刮削内容")
				return
			}
			a.scraper.mu.Lock()
			defer a.scraper.mu.Unlock()
			if (a.scraper.running || a.scraper.planning) && !a.scraper.automatic && config.Enabled {
				fail(w, http.StatusConflict, "手动刮削任务运行中，请等待完成")
				return
			}
			previous := a.scraperSettings()
			data, err := json.Marshal(config)
			if err == nil {
				_, err = a.db.Exec("INSERT INTO settings(k,v) VALUES('scraper',?) ON CONFLICT(k) DO UPDATE SET v=excluded.v", string(data))
			}
			if err != nil {
				fail(w, http.StatusInternalServerError, "保存失败")
				return
			}
			if previous.MonitorAutoRefresh && !config.MonitorAutoRefresh && !a.defaultOn("watch_enabled") {
				a.cancelScraperRefreshQueue()
			}
			a.scraper.plan = nil
			if (!config.Enabled || !config.MonitorEnabled && a.scraper.automatic) && a.scraper.cancel != nil {
				a.scraper.cancel()
			}
			state := func(enabled bool) string {
				if enabled {
					return "开启"
				}
				return "关闭"
			}
			reason := "设置已保存；刮削" + state(config.Enabled) + "；实时监控" + state(config.MonitorEnabled) + "；自动刷新" + state(config.MonitorAutoRefresh)
			a.scraperPhase("保存", scraperScope{}, MediaRecognition{}, config.Scraper, reason)
			if previous.Enabled != config.Enabled || previous.MonitorEnabled != config.MonitorEnabled || previous.MonitorAutoRefresh != config.MonitorAutoRefresh {
				a.scraperPhase("开关", scraperScope{}, MediaRecognition{}, config.Scraper, "刮削"+state(config.Enabled)+"；实时监控"+state(config.MonitorEnabled)+"；自动刷新"+state(config.MonitorAutoRefresh))
			}
			respond(w, config)
		default:
			fail(w, http.StatusMethodNotAllowed, "GET/PUT required")
		}
	case "/admin/scraper/plan":
		if r.Method == http.MethodGet {
			a.scraper.mu.Lock()
			planning, plan, planError := a.scraper.planning, a.scraper.plan, a.scraper.planError
			a.scraper.mu.Unlock()
			response := M{"Planning": planning, "Plan": plan, "Error": planError}
			if plan != nil && r.URL.Query().Get("preview") == "true" {
				preview := *plan
				failed := 0
				for _, object := range plan.Objects {
					if object.Error != "" {
						failed++
					}
				}
				response["TotalObjects"], response["FailedObjects"] = len(plan.Objects), failed
				if len(preview.Objects) > 100 {
					preview.Objects = preview.Objects[:100]
				}
				response["Plan"] = &preview
			}
			respond(w, response)
			return
		}
		if r.Method != http.MethodPost {
			fail(w, http.StatusMethodNotAllowed, "POST required")
			return
		}
		var request struct {
			ItemID            string
			ManualRecognition *MediaRecognition
			IssueIDs          []string
		}
		if !body(w, r, &request) {
			return
		}
		query := r.URL.Query()
		if request.IssueIDs != nil {
			if len(request.IssueIDs) == 0 || len(request.IssueIDs) > 100 {
				fail(w, http.StatusBadRequest, "请选择1–100个待处理项目")
				return
			}
			if request.ItemID != "" || request.ManualRecognition != nil || query.Has("file") || query.Has("root") {
				fail(w, http.StatusBadRequest, "批量重刮不能同时指定文件或手动识别")
				return
			}
			unique := make([]string, 0, len(request.IssueIDs))
			seen := make(map[string]bool, len(request.IssueIDs))
			for _, issueID := range request.IssueIDs {
				if len(issueID) != 32 || strings.Trim(issueID, "0123456789abcdef") != "" {
					fail(w, http.StatusBadRequest, "无效待处理项目")
					return
				}
				if !seen[issueID] {
					seen[issueID] = true
					unique = append(unique, issueID)
				}
			}
			request.IssueIDs = unique
		}
		var scope *scraperFileScope
		if request.ItemID != "" {
			if query.Has("file") {
				fail(w, http.StatusBadRequest, "不能同时指定文件和媒体")
				return
			}
			item, err := a.item(request.ItemID)
			if err != nil || item.Kind != "Movie" && item.Kind != "Series" {
				fail(w, http.StatusBadRequest, "目标不是电影或剧集")
				return
			}
			if recognition := request.ManualRecognition; recognition != nil {
				recognition.Title = strings.TrimSpace(recognition.Title)
				if recognition.Title == "" || recognition.Year < 0 || recognition.Year > 9999 || len(recognition.TMDBID) > 20 || strings.Trim(recognition.TMDBID, "0123456789") != "" {
					fail(w, http.StatusBadRequest, "手动识别参数无效")
					return
				}
				recognition.Kind = item.Kind
			}
			scope, err = a.scraperItemScope(item.Lib, item.Path)
			if err != nil {
				fail(w, http.StatusBadRequest, "目标路径不可刮削")
				return
			}
		}
		if query.Has("file") {
			var err error
			scope, err = a.scraperManagedFileScope(query.Get("file"), query.Get("root"))
			if err != nil {
				fail(w, http.StatusBadRequest, "目标路径不可刮削："+err.Error())
				return
			}
		}
		a.scraper.mu.Lock()
		if a.scraper.running || a.scraper.planning {
			a.scraper.mu.Unlock()
			fail(w, http.StatusConflict, "任务运行中")
			return
		}
		config := a.scraperSettings()
		if !config.Enabled {
			a.scraper.mu.Unlock()
			fail(w, http.StatusConflict, "请先开启刮削服务")
			return
		}
		config.fileScope, config.itemID, config.manualRecognition, config.taskID = scope, request.ItemID, request.ManualRecognition, id()
		config.issueIDs = request.IssueIDs
		a.scraper.taskID.Store(&config.taskID)
		a.scraper.plan, a.scraper.planError, a.scraper.planning = nil, "", true
		parent := r.Context()
		async := query.Get("async") == "true"
		if async {
			parent = context.Background()
		}
		ctx, cancel := context.WithCancel(parent)
		a.scraper.cancel = cancel
		a.scraper.mu.Unlock()
		a.scraperPhase("开启扫描", scraperScope{}, MediaRecognition{}, config.Scraper, "手动任务扫描已开启")
		build := func() (*scraperPlan, error) {
			defer cancel()
			plan, err := a.buildScraperPlan(ctx, config)
			a.scraper.mu.Lock()
			if ctx.Err() != nil {
				plan, err = nil, errors.New("任务扫描已取消")
			}
			a.scraper.planning, a.scraper.cancel, a.scraper.plan = false, nil, plan
			if err != nil {
				a.scraper.planError = err.Error()
			}
			a.scraper.mu.Unlock()
			if err == nil {
				a.scraperPhase("扫描任务完成", scraperScope{}, MediaRecognition{}, config.Scraper, "手动计划已生成")
			} else {
				a.scraperPhase("扫描未完成", scraperScope{}, MediaRecognition{}, config.Scraper, err.Error())
			}
			return plan, err
		}
		if async {
			go func() { _, _ = build() }()
			respond(w, M{"Planning": true, "TaskID": config.taskID})
			return
		}
		plan, err := build()
		if err != nil {
			fail(w, http.StatusBadRequest, err.Error())
			return
		}
		respond(w, plan)
	case "/admin/scraper/tree":
		a.scraperTree(w, r)
	case "/admin/scraper/start":
		if r.Method != http.MethodPost {
			fail(w, http.StatusMethodNotAllowed, "POST required")
			return
		}
		var request struct{ ID string }
		if !body(w, r, &request) {
			return
		}
		a.scraper.mu.Lock()
		defer a.scraper.mu.Unlock()
		config := a.scraperSettings()
		plan := a.scraper.plan
		if a.scraper.running || a.scraper.planning || plan == nil || plan.ID != request.ID || !config.Enabled {
			a.scraperPhase("失败", scraperScope{}, MediaRecognition{}, "", "启动被拒绝：总开关关闭、计划无效或任务运行中")
			fail(w, http.StatusConflict, "计划无效或任务正在运行，请重新扫描刮削任务")
			return
		}
		a.scraper.plan, a.scraper.running = nil, true
		a.scraper.taskID.Store(&plan.ID)
		ctx, cancel := context.WithCancel(context.Background())
		a.scraper.cancel = cancel
		go func() { defer cancel(); _ = a.runScraper(ctx, plan) }()
		respond(w, M{"Started": true})
	case "/admin/scraper/cancel":
		if r.Method != http.MethodPost {
			fail(w, http.StatusMethodNotAllowed, "POST required")
			return
		}
		a.scraper.mu.Lock()
		if a.scraper.cancel != nil {
			a.scraper.cancel()
		}
		a.scraper.plan = nil
		a.scraper.mu.Unlock()
		a.scraperPhase("停止扫描", scraperScope{}, MediaRecognition{}, "", "已请求停止扫描或刮削任务")
		respond(w, M{"Cancelled": true})
	case "/admin/scraper/control":
		a.scraperControl(w, r)
	default:
		fail(w, http.StatusNotFound, "not found")
	}
}

var (
	scraperRegistryMu sync.RWMutex
	scraperRegistry   = make(map[string]Scraper)

	scraperCategoryContents = map[string][]string{
		"Movie":   {"NFO", "Poster", "Backdrop", "Logo", "Disc", "Banner"},
		"Series":  {"NFO", "Poster", "Backdrop", "Logo", "Banner"},
		"Season":  {"NFO", "Poster", "Banner"},
		"Episode": {"NFO", "Still"},
	}

	scraperURLPattern        = regexp.MustCompile(`(?i)https?://[^\s<>"']+`)
	scraperCredentialPattern = regexp.MustCompile(`(?i)(api[_-]?key|access[_-]?key|key|token|password|passwd|sign(?:ature)?|authorization|secret|credential)\s*[:=]\s*[^\s,;]+`)
)

// RegisterScraper rejects nil, unnamed and duplicate providers.
func RegisterScraper(scraper Scraper) {
	scraperRegistryMu.Lock()
	defer scraperRegistryMu.Unlock()
	if scraper == nil || scraper.Name() == "" {
		panic("invalid scraper")
	}
	if _, exists := scraperRegistry[scraper.Name()]; exists {
		panic("duplicate scraper")
	}
	scraperRegistry[scraper.Name()] = scraper
}

func lookupScraper(name string) Scraper {
	scraperRegistryMu.RLock()
	defer scraperRegistryMu.RUnlock()
	return scraperRegistry[name]
}

func (a *App) scraperSettings() scraperConfig {
	config := scraperConfig{
		Scraper:            "TMDB",
		ChineseMetadata:    true,
		MonitorAutoRefresh: true,
		Concurrency:        1,
		Categories:         make(map[string]scraperCategory, len(scraperCategoryContents)),
	}
	for category, content := range scraperCategoryContents {
		config.Categories[category] = scraperCategory{
			Enabled: true,
			Content: append([]string(nil), content[:2]...),
		}
	}

	var raw []byte
	if err := a.db.QueryRow("SELECT v FROM settings WHERE k='scraper'").Scan(&raw); err == nil {
		_ = json.Unmarshal(raw, &config)
	}
	config.Concurrency = scraperConcurrency(config.Concurrency)
	return config
}

func validScraperConfig(config scraperConfig) bool {
	if config.Concurrency < 0 || config.Concurrency > maxScraperConcurrency {
		return false
	}
	if !validScraperSelection(config) || len(config.Categories) != len(scraperCategoryContents) {
		return false
	}
	for category, setting := range config.Categories {
		allowed, exists := scraperCategoryContents[category]
		if !exists {
			return false
		}
		seen := make(map[string]bool, len(setting.Content))
		for _, content := range setting.Content {
			valid := false
			for _, candidate := range allowed {
				if content == candidate {
					valid = true
					break
				}
			}
			if !valid || seen[content] {
				return false
			}
			seen[content] = true
		}
	}
	return true
}

// scraperRoot checks that the target remains inside a configured library path.
func (a *App) scraperRoot(libraryID, targetPath string) (string, error) {
	var libraryPath string
	if err := a.db.QueryRow("SELECT path FROM libraries WHERE id=?", libraryID).Scan(&libraryPath); err != nil {
		return "", errors.New("媒体库不存在")
	}
	for _, root := range a.libraryPaths(libraryID, libraryPath) {
		relativePath, err := filepath.Rel(root, targetPath)
		if err == nil && filepath.IsLocal(relativePath) {
			return root, nil
		}
	}
	return "", errors.New("媒体已移出配置目录")
}

func scraperFilename(artwork, kind, path string) string {
	directory := filepath.Dir(path)
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	if kind == "Series" {
		directory, base = path, "tvshow"
	} else if kind == "Season" {
		directory, base = path, "season"
	}

	name := ""
	switch artwork {
	case "NFO":
		name = base + ".nfo"
	case "Disc":
		name = "disc.png"
	case "Logo":
		name = "logo.png"
	case "Still":
		name = base + ".jpg"
	case "Banner":
		name = "banner.jpg"
	case "Poster":
		name = "poster.jpg"
	case "Backdrop":
		name = "backdrop.jpg"
	}
	if name == "" {
		return ""
	}
	return filepath.Join(directory, name)
}

func scraperCandidates(artwork, kind, path string, season int) []string {
	primary := scraperFilename(artwork, kind, path)
	directory := filepath.Dir(path)
	stem := strings.TrimSuffix(path, filepath.Ext(path))
	if kind == "Series" {
		stem = filepath.Join(path, "tvshow")
	} else if kind == "Season" {
		stem = filepath.Join(path, "season")
	}

	var candidates []string
	switch {
	case artwork == "NFO":
		candidates = append(candidates, primary)
		if kind == "Movie" {
			candidates = append(candidates, filepath.Join(directory, "movie.nfo"))
		}
	case artwork == "Still":
		for _, extension := range []string{"-thumb.jpg", ".jpg", ".jpeg", ".png", ".webp"} {
			candidates = append(candidates, stem+extension)
		}
	case artwork == "Poster" && kind == "Season":
		for _, extension := range []string{".jpg", ".png", ".webp", ".jpeg"} {
			candidates = append(candidates,
				filepath.Join(directory, "poster"+extension),
				filepath.Join(directory, "folder"+extension),
				filepath.Join(directory, fmt.Sprintf("season%02d-poster%s", season, extension)),
				filepath.Join(directory, fmt.Sprintf("season%d-poster%s", season, extension)),
			)
		}
	case artwork == "Poster" && kind == "Movie":
		candidates = append(candidates,
			filepath.Join(directory, "poster.jpg"),
			filepath.Join(directory, "folder.jpg"),
			filepath.Join(directory, "poster.png"),
			stem+"-poster.jpg",
			stem+".jpg",
		)
	}

	aliases := map[string][]string{
		"Poster":   {"poster", "folder"},
		"Backdrop": {"backdrop", "fanart"},
		"Logo":     {"logo", "clearlogo"},
		"Disc":     {"disc", "cdart"},
		"Banner":   {"banner"},
	}
	if artwork != "Poster" || kind != "Season" {
		for _, alias := range aliases[artwork] {
			for _, extension := range []string{".jpg", ".png", ".webp", ".jpeg"} {
				if kind == "Movie" {
					candidates = append(candidates, stem+"-"+alias+extension)
				}
				candidates = append(candidates, filepath.Join(directory, alias+extension))
			}
		}
	}

	// Try legacy aliases before the canonical filename.
	return append(candidates, primary)
}

func scraperFileState(libraryPath, targetPath string) (bool, error) {
	root, relativePath, err := scraperOpen(libraryPath, targetPath)
	if err != nil {
		return false, err
	}
	defer root.Close()

	info, err := root.Lstat(relativePath)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil || !info.Mode().IsRegular() {
		return false, errors.New("目标不是普通文件")
	}
	return true, nil
}

// Prefer an existing legacy filename; otherwise return the canonical target.
func scraperExistingTarget(libraryRoot, artwork, kind, path string, season int) (string, bool, error) {
	for _, candidate := range scraperCandidates(artwork, kind, path, season) {
		found, err := scraperFileState(libraryRoot, candidate)
		if err != nil {
			return "", false, err
		}
		if found {
			return candidate, true, nil
		}
	}
	return scraperFilename(artwork, kind, path), false, nil
}

func (a *App) scraperIgnore(path string) bool {
	a.scraper.eventMu.Lock()
	defer a.scraper.eventMu.Unlock()

	suppression, exists := a.scraper.suppressed[path]
	if !exists {
		return false
	}
	if time.Now().After(suppression.until) {
		delete(a.scraper.suppressed, path)
		return false
	}
	if suppression.stamp != nil {
		current, err := os.Lstat(path)
		if err != nil || !os.SameFile(suppression.stamp, current) || suppression.stamp.Size() != current.Size() || !suppression.stamp.ModTime().Equal(current.ModTime()) {
			delete(a.scraper.suppressed, path)
			return false
		}
	}
	return true
}

func (a *App) scraperSuppress(path string) {
	a.scraper.eventMu.Lock()
	defer a.scraper.eventMu.Unlock()
	if a.scraper.suppressed == nil {
		a.scraper.suppressed = make(map[string]scraperSuppression)
	}

	now := time.Now()
	for existing, suppression := range a.scraper.suppressed {
		if now.After(suppression.until) {
			delete(a.scraper.suppressed, existing)
		}
	}
	a.scraper.suppressed[path] = scraperSuppression{until: now.Add(time.Minute)}
}

func (a *App) scraperWritten(path string, stamp os.FileInfo) {
	a.scraper.eventMu.Lock()
	defer a.scraper.eventMu.Unlock()
	if stamp == nil {
		delete(a.scraper.suppressed, path)
		return
	}
	a.scraper.suppressed[path] = scraperSuppression{
		until: time.Now().Add(time.Minute),
		stamp: stamp,
	}
}

func (a *App) scraperSafeError(err error) error {
	if err == nil {
		return nil
	}
	return errors.New(scraperSanitize(err.Error(), a.tmdbSettings().APIKey))
}

func scraperSanitize(message, secret string) string {
	if secret != "" {
		message = strings.ReplaceAll(message, secret, "[已隐藏]")
	}
	message = scraperURLPattern.ReplaceAllString(message, "[URL 已隐藏]")
	message = scraperCredentialPattern.ReplaceAllString(message, "$1=[已隐藏]")
	if len(message) > maxScraperErrorBytes {
		runes := []rune(message)
		if len(runes) > maxScraperErrorRunes {
			runes = runes[:maxScraperErrorRunes]
		}
		message = string(runes)
	}
	return message
}

// scraperOpen confines writes to the library root and rejects symlinks.
func scraperOpen(libraryPath, targetPath string) (*os.Root, string, error) {
	if !filepath.IsAbs(libraryPath) || !filepath.IsAbs(targetPath) || filepath.Clean(targetPath) != targetPath {
		return nil, "", errors.New("拒绝链接或不可访问路径")
	}

	relativePath, err := filepath.Rel(libraryPath, targetPath)
	if err != nil || relativePath == "." || !filepath.IsLocal(relativePath) {
		return nil, "", errors.New("拒绝链接或不可访问路径")
	}

	root, err := scraperLibraryRoot(libraryPath)
	if err != nil {
		return nil, "", scraperFilesystemError("无法打开媒体库目录", err)
	}

	parts := strings.Split(relativePath, string(filepath.Separator))
	current := "."
	for i, part := range parts {
		current = filepath.Join(current, part)
		info, statErr := root.Lstat(current)
		if errors.Is(statErr, fs.ErrNotExist) && i == len(parts)-1 {
			break
		}
		if statErr != nil || info.Mode()&fs.ModeSymlink != 0 {
			_ = root.Close()
			return nil, "", errors.New("拒绝链接或不可访问路径")
		}
	}

	return root, relativePath, nil
}

// Build the plan from the media index; fetch provider data only during execution.
func (a *App) buildScraperPlan(ctx context.Context, config scraperConfig) (plan *scraperPlan, err error) {
	if !config.Enabled {
		a.scraperPhase("跳过", scraperScope{}, MediaRecognition{}, config.Scraper, "刮削总开关关闭，禁止生成计划")
		return nil, errors.New("请先开启刮削")
	}
	activityID := a.newActivity("scraper", "", "扫描刮削任务 · "+config.Scraper)
	defer func() { a.finishActivity(activityID, err) }()
	a.changeActivity(activityID, func(entry *activityEntry) { entry.State = "counting" })
	if len(config.issueIDs) > 0 {
		items, skipped, err := a.scraperIssueItems(ctx, config.issueIDs)
		if err != nil {
			return nil, err
		}
		if len(items) == 0 {
			return nil, errors.New("所选项目已处理、媒体已变更或尚未加入媒体库，请刷新待处理列表并检查索引")
		}
		plan, err := a.buildScraperItems(ctx, config, items, activityID)
		if err == nil {
			plan.RetrySelected, plan.RetrySkipped = len(config.issueIDs), skipped
		}
		return plan, err
	}

	query := "SELECT id,lib,parent,name,kind,path,url,overview,poster,year,season,episode,mtime,size,added_at,premiere_date,sort_name,random_key FROM items WHERE kind IN ('Movie','Series','Season','Episode')"
	var args []any
	if config.itemID != "" {
		query += " AND id=$1"
		args = append(args, config.itemID)
	}
	query += " ORDER BY path,kind"
	rows, queryErr := a.db.QueryContext(ctx, query, args...)
	if queryErr != nil {
		return nil, errors.New("读取媒体索引失败")
	}
	var items []Item
	for rows.Next() {
		item, readErr := readItem(rows)
		if readErr != nil {
			_ = rows.Close()
			return nil, errors.New("读取媒体索引失败")
		}
		if config.fileScope.contains(item.Lib, item.Path) {
			items = append(items, item)
		}
	}
	readErr := rows.Err()
	_ = rows.Close()
	if readErr != nil {
		return nil, errors.New("读取媒体索引失败")
	}
	return a.buildScraperItems(ctx, config, items, activityID)
}

func (a *App) buildScraperItems(ctx context.Context, config scraperConfig, items []Item, activityID string) (*scraperPlan, error) {
	if !config.Enabled {
		return nil, errors.New("刮削总开关关闭")
	}
	plan := &scraperPlan{ID: id(), Config: config, Objects: []scraperObject{}}
	if config.taskID != "" {
		plan.ID = config.taskID
	}
	rootsByLibrary := make(map[string][]string)
	seenTargets := make(map[string]bool)
	secret := a.tmdbSettings().APIKey
	safe := func(value string) string { return scraperSanitize(value, secret) }
	lastDirectory := ""

	for index, item := range items {
		if ctx.Err() != nil {
			return nil, errors.New("任务扫描已取消")
		}
		roots, cached := rootsByLibrary[item.Lib]
		if !cached {
			var rootPath, raw string
			if err := a.db.QueryRowContext(ctx, "SELECT path FROM libraries WHERE id=$1", item.Lib).Scan(&rootPath); err == nil {
				err := a.db.QueryRowContext(ctx, "SELECT v FROM settings WHERE k=$1", "library-paths:"+item.Lib).Scan(&raw)
				if err != nil || json.Unmarshal([]byte(raw), &roots) != nil {
					roots = []string{rootPath}
				}
			}
			rootsByLibrary[item.Lib] = roots
		}

		libraryRoot := ""
		rootFound := false
		for _, rootPath := range roots {
			relative, err := filepath.Rel(rootPath, item.Path)
			if err == nil && (relative == "." || filepath.IsLocal(relative)) {
				libraryRoot, rootFound = rootPath, true
				break
			}
		}
		directory := item.Path
		if item.Kind == "Movie" || item.Kind == "Episode" {
			directory = filepath.Dir(item.Path)
		}
		relative, relativeErr := filepath.Rel(libraryRoot, directory)
		if rootFound && relativeErr == nil && config.fileScope == nil && len(config.issueIDs) == 0 &&
			!scraperScopeEnabled(config.ManualScopes, item.Lib, libraryRoot, relative, true) {
			if directory != lastDirectory {
				a.changeActivity(activityID, func(entry *activityEntry) {
					entry.Current = "跳过目录：" + safe(directory) + " · 目录未选择"
					entry.Total, entry.Done = len(items), index+1
				})
				lastDirectory = directory
			}
			continue
		}
		if directory != lastDirectory {
			a.changeActivity(activityID, func(entry *activityEntry) {
				entry.Current = "正在扫描目录：" + safe(directory)
				entry.Total, entry.Done = len(items), index
			})
			lastDirectory = directory
		}

		object := scraperObject{Item: item, ID: item.ID, Name: item.Name, Kind: item.Kind, Targets: []scraperTarget{}}
		if item.ID == config.itemID && config.manualRecognition != nil {
			recognition := *config.manualRecognition
			recognition.Kind, recognition.Matched = item.Kind, true
			object.Recognition = &recognition
		}
		category := config.Categories[item.Kind]
		if !category.Enabled {
			object.Disabled = true
			plan.Disabled++
		} else {
			accessible := rootFound
			if accessible {
				root, local, err := scraperOpen(libraryRoot, item.Path)
				if err != nil {
					accessible = false
				} else {
					_, err = root.Stat(local)
					_ = root.Close()
					accessible = err == nil
				}
			}
			if !accessible {
				object.Error = "媒体路径不可访问或不安全"
			} else {
				for _, content := range category.Content {
					path, exists, err := scraperExistingTarget(libraryRoot, content, item.Kind, item.Path, item.Season)
					if err != nil {
						object.Error = err.Error()
						break
					}
					action := "create"
					if seenTargets[path] || (exists && (!config.Overwrite || strings.EqualFold(filepath.Ext(path), ".webp"))) {
						action = "skip"
					} else if exists {
						action = "overwrite"
					}
					seenTargets[path] = true
					object.Targets = append(object.Targets, scraperTarget{Content: content, Path: path, Action: action})
				}
			}
		}
		plan.Objects = append(plan.Objects, object)
		a.changeActivity(activityID, func(entry *activityEntry) {
			entry.Name = safe(item.Name)
			entry.Current = "正在扫描目录：" + safe(directory) + " · " + item.Kind + " · " + config.Scraper
			entry.Total, entry.Done = len(items), index+1
		})
	}
	// Recount only the enabled objects.
	plan.Pending, plan.Skipped, plan.Overwrite = 0, 0, 0
	for _, object := range plan.Objects {
		if object.Disabled {
			continue
		}
		for _, target := range object.Targets {
			switch target.Action {
			case "create":
				plan.Pending++
			case "skip":
				plan.Skipped++
			case "overwrite":
				plan.Overwrite++
			}
		}
	}
	return plan, nil
}

func (a *App) runScraper(ctx context.Context, plan *scraperPlan) error {
	a.scraper.taskID.Store(&plan.ID)
	defer func() {
		a.scraper.mu.Lock()
		defer a.scraper.mu.Unlock()
		a.scraper.running = false
		if a.scraper.pause != nil {
			close(a.scraper.pause)
			a.scraper.pause = nil
		}
		a.scraper.automatic = false
		a.scraper.cancel = nil
	}()
	selection := plan.Config.Scrapers
	if selection == nil {
		selection = []string{plan.Config.Scraper}
	}
	activityID := a.newActivity("scraper", plan.ID, "刮削 · "+strings.Join(selection, " → "))
	provider := orderedScrapers(selection)
	if concurrency := scraperConcurrency(plan.Config.Concurrency); concurrency > 1 {
		return a.runScraperConcurrent(ctx, plan, activityID, provider, concurrency)
	}
	failures := 0
	for index, object := range plan.Objects {
		if err := a.waitScraper(ctx, activityID); err != nil {
			a.finishActivity(activityID, err)
			return err
		}
		if err := ctx.Err(); err != nil {
			a.finishActivity(activityID, errors.New("刮削已取消"))
			return err
		}
		safeName := a.scraperSafeError(errors.New(object.Name)).Error()
		directory := a.scraperSafeError(errors.New(filepath.Dir(object.Item.Path))).Error()
		a.changeActivity(activityID, func(entry *activityEntry) {
			entry.State = "running"
			entry.Total, entry.Done = len(plan.Objects), index
			entry.Name = safeName
			entry.Current = "正在刮削目录：" + directory + " · " + object.Kind + " · " + plan.Config.Scraper
			entry.Progress = float64(index) * 100 / float64(len(plan.Objects))
		})
		if object.Disabled {
			continue
		}
		itemActivity := a.newActivity("scraper", object.ID, safeName)
		err := a.scrapeObject(ctx, plan.Config, object, provider, itemActivity)
		a.finishActivity(itemActivity, a.scraperSafeError(err))
		if err != nil {
			failures++
		}
		root, _ := a.scraperRoot(object.Item.Lib, object.Item.Path)
		relative, _ := filepath.Rel(root, object.Item.Path)
		recognition := MediaRecognition{Kind: object.Kind, Title: object.Name}
		if object.Recognition != nil {
			recognition = *object.Recognition
		}
		phase, reason := "成功", "单媒体刮削结束"
		if err != nil {
			phase, reason = "失败", err.Error()
		}
		a.scraperPhase(phase, scraperScope{Library: object.Item.Lib, Root: root, Relative: relative}, recognition, plan.Config.Scraper, reason)
	}
	if err := ctx.Err(); err != nil {
		a.finishActivity(activityID, errors.New("刮削已取消"))
		return err
	}
	a.changeActivity(activityID, func(entry *activityEntry) {
		entry.Total, entry.Done = len(plan.Objects), len(plan.Objects)
		entry.Progress = 100
		entry.Current = fmt.Sprintf("完成，失败 %d", failures)
	})
	var err error
	if failures > 0 {
		err = fmt.Errorf("%d 个媒体失败，详见单片日志", failures)
	}
	a.finishActivity(activityID, err)
	return err
}

func (a *App) scrapeObject(ctx context.Context, config scraperConfig, object scraperObject, provider Scraper, activityID string) (outcome error) {
	var pendingIssue error
	defer func() {
		if outcome != nil {
			a.scraperStoreIssue(ctx, object, outcome)
		} else {
			a.scraperStoreIssue(ctx, object, pendingIssue)
		}
	}()
	if object.Error != "" {
		return errors.New(object.Error)
	}
	libraryRoot, err := a.scraperRoot(object.Item.Lib, object.Item.Path)
	if err != nil {
		return err
	}
	item, err := a.item(object.ID)
	if err != nil || item.Path != object.Item.Path || item.Kind != object.Kind ||
		item.Lib != object.Item.Lib || item.Season != object.Item.Season || item.Episode != object.Item.Episode {
		return errors.New("媒体发生变化，请重新生成计划")
	}
	root, local, err := scraperOpen(libraryRoot, item.Path)
	if err != nil {
		return err
	}
	_, err = root.Stat(local)
	_ = root.Close()
	if err != nil {
		return errors.New("媒体文件已不存在")
	}
	if recognition := object.Recognition; recognition != nil {
		item.scraperTMDBID = recognition.TMDBID
		if item.Kind != recognition.Kind {
			return errors.New("识别类型与索引不一致")
		}
		if item.Kind == "Movie" || item.Kind == "Series" {
			if recognition.Title != "" {
				item.Name = recognition.Title
			}
			if recognition.Year > 0 {
				item.Year = recognition.Year
			}
		}
	}

	changed := false
	paths := []string{item.Path}
	recognition := MediaRecognition{Kind: item.Kind, Title: item.Name}
	if object.Recognition != nil {
		recognition = *object.Recognition
	}
	scope := scraperScope{Library: item.Lib, Root: libraryRoot, Relative: filepath.Dir(local)}
	phase := func(name, reason string) { a.scraperPhase(name, scope, recognition, config.Scraper, reason) }
	defer func() {
		if !changed {
			return
		}
		refreshPaths := append([]string(nil), paths...)
		refreshPaths = append(refreshPaths, filepath.Dir(item.Path))
		a.scraper.mu.Lock()
		automatic, running := a.scraper.automatic, a.scraper.running
		a.scraper.mu.Unlock()
		if !running {
			paths = a.takeQueuedRefresh(item.Lib, paths)
			a.markScraperRefresh(item.Lib, refreshPaths)
			phase("刷新", "手动刮削单媒体写入完成，立即精准刷新")
			done := make(chan struct{})
			go func() {
				defer close(done)
				a.refreshMediaPaths(item.Lib, paths)
			}()
			timer := time.NewTimer(30 * time.Second)
			defer timer.Stop()
			select {
			case <-timer.C:
				phase("等待刷新", "等待局部刷新超时；原刷新继续处理")
			case <-ctx.Done():
				phase("等待刷新", "刮削已取消；已写入文件的局部刷新继续完成")
			case <-done:
			}
			return
		}
		if automatic && a.automaticRefreshOwner() == "" {
			return
		}
		a.queueBatchRefresh(item.Lib, paths, !automatic)
		phase("等待刷新", fmt.Sprintf("已加入合并刷新队列；批次结束后等待至少 %d 秒稳定再统一精准刷新", a.watchDelay()))
	}()

	fetchWrite := func(target scraperTarget, targetActivity, current, safePath string) error {
		update := func(value string) {
			a.changeActivity(targetActivity, func(entry *activityEntry) { entry.Current = value })
		}
		if target.Action == "skip" {
			update(current + " · 跳过已有文件")
			phase("跳过", target.Content+" · 已有文件 · 文件："+safePath)
			return nil
		}
		if !scraperAllowedTarget(target, item) {
			return errors.New("目标文件不在允许列表")
		}
		exists, err := scraperFileState(libraryRoot, target.Path)
		if err != nil {
			return err
		}
		if !config.Overwrite {
			_, exists, err = scraperExistingTarget(libraryRoot, target.Content, item.Kind, item.Path, item.Season)
			if err != nil {
				return err
			}
		}
		if exists && !config.Overwrite {
			update(current + " · 跳过已有文件")
			phase("跳过", target.Content+" · 已有文件 · 文件："+safePath)
			return nil
		}
		action := "下载"
		if exists {
			action = "覆盖下载"
		}
		update(current + " · " + action)
		fetchCtx, cancel := context.WithTimeout(context.WithValue(ctx, scraperPreferencesKey{}, config), 45*time.Second)
		defer cancel()
		phase(action, target.Content+" · "+action+" · 文件："+safePath)
		data, err := provider.Fetch(fetchCtx, a, item, target.Content)
		if errors.Is(err, errTMDBNoArtwork) {
			reason := target.Content + " · 无可用图片，保留已有文件"
			if item.Kind == "Episode" && target.Content == "Still" {
				reason = "单集图 · 暂无可用图片，正常跳过"
			}
			update(reason)
			phase("跳过", reason)
			return nil
		}
		if errors.Is(err, errTMDBEpisodeNotFound) {
			reason := target.Content + " · TMDB 无此单集，可能存在分集版本或编号差异；保留已有文件，请核对集数"
			pendingIssue = errors.New(reason)
			update(reason)
			phase("跳过", reason)
			return nil
		}
		if err != nil {
			return a.scraperSafeError(err)
		}
		if fetchCtx.Err() != nil {
			return errors.New("请求已取消或超时")
		}
		phase("已下载", fmt.Sprintf("%s · %d 字节 · 准备保存：%s", target.Content, len(data), safePath))
		// Hold the configuration lock while validating the root and writing the file.
		a.libraryConfig.Lock()
		currentRoot, rootErr := a.scraperRoot(item.Lib, item.Path)
		if fetchCtx.Err() != nil {
			a.libraryConfig.Unlock()
			return errors.New("请求已取消或超时")
		}
		if rootErr != nil || currentRoot != libraryRoot {
			a.libraryConfig.Unlock()
			return errors.New("媒体库目录配置已改变")
		}
		err = a.scraperWrite(libraryRoot, target, item, data, config.Overwrite)
		a.libraryConfig.Unlock()
		if errors.Is(err, fs.ErrExist) {
			update(current + " · 跳过执行时新增文件")
			phase("跳过", target.Content+" · 执行时文件已存在 · 文件："+safePath)
			return nil
		}
		if err != nil {
			return fmt.Errorf("写入文件失败：%w", err)
		}
		changed = true
		if item.Kind == "Movie" || item.Kind == "Episode" || !strings.HasPrefix(target.Path, item.Path+"/") {
			paths = append(paths, target.Path)
		}
		update(fmt.Sprintf("%s · %s成功 · 已保存：%s · %d 字节", target.Content, action, safePath, len(data)))
		phase("写入成功", fmt.Sprintf("%s · %s成功 · 文件：%s · %d 字节", target.Content, action, safePath, len(data)))
		return nil
	}

	var failures []string
	for _, target := range object.Targets {
		if err := a.waitScraper(ctx, activityID); err != nil {
			return err
		}
		if ctx.Err() != nil {
			return errors.New("刮削已取消")
		}
		safePath := a.scraperSafeError(errors.New(target.Path)).Error()
		current := object.Kind + " · " + provider.Name() + " · " + target.Content + " · 文件：" + safePath
		a.changeActivity(activityID, func(entry *activityEntry) { entry.Current = current })
		targetActivity := a.newActivity("scraper", object.ID, a.scraperSafeError(errors.New(object.Name)).Error())
		a.changeActivity(targetActivity, func(entry *activityEntry) { entry.Current = current })
		err := a.scraperSafeError(fetchWrite(target, targetActivity, current, safePath))
		if err != nil {
			phase("失败", target.Content+" · 文件："+safePath+" · 原因："+err.Error())
			failures = append(failures, target.Content+" · 文件："+safePath+" · "+err.Error())
		}
		a.finishActivity(targetActivity, err)
	}
	if len(failures) > 0 {
		return errors.New(strings.Join(failures, "；"))
	}
	return nil
}
