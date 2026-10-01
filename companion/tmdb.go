package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var tmdbTaggedID = regexp.MustCompile(`(?i)\{tmdb(?:id)?-([0-9]+)\}`)

func (a *App) tmdbSettings() tmdbConfig {
	dataRoot := os.Getenv("MEDIA_INFO_ROOT")
	if dataRoot == "" {
		dataRoot = "/app/data"
	}

	settings := tmdbConfig{
		APIBase:           "https://api.themoviedb.org/3",
		Directory:         filepath.Join(dataRoot, "tmdb"),
		RequestsPerSecond: 15,
	}
	var stored string
	if err := a.db.QueryRow("SELECT v FROM settings WHERE k='tmdb'").Scan(&stored); err == nil {
		_ = json.Unmarshal([]byte(stored), &settings)
	}
	settings.Valid = false
	settings.Validation = ""
	return settings
}

func (a *App) tmdbLock(ctx context.Context) bool {
	a.tmdb.once.Do(func() {
		a.tmdb.gate = make(chan struct{}, 1)
	})
	select {
	case a.tmdb.gate <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

func (a *App) tmdbUnlock() {
	<-a.tmdb.gate
}

// tmdbIdentity returns the metadata cache key and the TMDB API path for an
// item. Season and Episode records use their parent Series' provider ID while
// retaining their own season/episode path suffix.
//
// The binary confirms the `{tmdb-123}` / `{tmdbid-123}` tag, `movie`/`tv`
// endpoint prefixes, and the `|zh-CN|` cache namespace. The exact choice of
// display title used for the name-based fallback is inferred from the item
// fields retained in the Go ABI.
func (a *App) tmdbIdentity(item Item) (key, endpoint string) {
	original := item
	season, episode := item.Season, item.Episode

	switch item.Kind {
	case "Season":
		series, err := a.item(item.Parent)
		if err != nil || series.Kind != "Series" {
			return "", ""
		}
		item = series
	case "Episode":
		seasonItem, err := a.item(item.Parent)
		if err != nil || seasonItem.Kind != "Season" {
			return "", ""
		}
		if season == 0 {
			season = seasonItem.Season
		}
		series, err := a.item(seasonItem.Parent)
		if err != nil || series.Kind != "Series" {
			return "", ""
		}
		item = series
	case "Movie", "Series":
	default:
		return "", ""
	}

	mediaType := "movie"
	if item.Kind == "Series" {
		mediaType = "tv"
	}

	metadata := a.metadata(item)
	providerID := strings.TrimSpace(metadata.TMDB)
	if providerID == "" {
		for _, uniqueID := range metadata.UniqueIDs {
			if strings.EqualFold(strings.TrimSpace(uniqueID.Type), "tmdb") {
				providerID = strings.TrimSpace(uniqueID.Value)
			}
		}
	}
	if providerID == "" {
		for _, candidate := range [...]string{item.Name, filepath.Base(item.Path)} {
			match := tmdbTaggedID.FindStringSubmatch(candidate)
			if len(match) == 2 {
				providerID = match[1]
				break
			}
		}
	}
	if providerID == "" {
		providerID = strings.TrimSpace(original.scraperTMDBID)
	}

	numericID, err := strconv.Atoi(providerID)
	if err != nil || numericID < 1 {
		display := scraperSearchIdentity(item.Name)
		title := display.Title
		if title == "" {
			title = strings.TrimSpace(item.Name)
		}
		year := item.Year
		if display.Year > 0 {
			year = display.Year
		}
		providerID = fmt.Sprintf("name:%s:%d", title, year)
	}

	suffix := ""
	if original.Kind == "Season" || original.Kind == "Episode" {
		suffix = fmt.Sprintf("/season/%d", season)
	}
	if original.Kind == "Episode" {
		suffix += fmt.Sprintf("/episode/%d", episode)
	}

	endpoint = mediaType + "/" + providerID + suffix
	cacheItemID := original.ID
	if cacheItemID == "" {
		cacheItemID = item.ID
	}
	key = cacheItemID + "|zh-CN|" + endpoint
	return key, endpoint
}

// resolveTMDBItem resolves the binary's name-based cache identity through a
// unique TMDB search result. The search thresholds and its ambiguity rule are
// reconstructed from the scraper pseudocode; the selected provider ID is kept
// on the local Item copy so season/episode requests can use their series ID.
func (a *App) resolveTMDBItem(ctx context.Context, item Item, settings tmdbConfig) (Item, string, string, error) {
	key, endpoint := a.tmdbIdentity(item)
	if key == "" || endpoint == "" {
		return item, "", "", errors.New("无可识别媒体 TMDB 身份")
	}
	if !strings.Contains(endpoint, "/name:") {
		return item, key, endpoint, nil
	}

	searchItem := item
	switch item.Kind {
	case "Season":
		if parent, err := a.item(item.Parent); err == nil && parent.Kind == "Series" {
			searchItem = parent
		}
	case "Episode":
		if season, err := a.item(item.Parent); err == nil && season.Kind == "Season" {
			if series, err := a.item(season.Parent); err == nil && series.Kind == "Series" {
				searchItem = series
			}
		}
	}

	identity := scraperSearchIdentity(searchItem.Name)
	title := strings.TrimSpace(identity.Title)
	if title == "" {
		title = strings.TrimSpace(filepath.Base(searchItem.Path))
	}
	year := identity.Year
	if year == 0 {
		year = searchItem.Year
	}
	mediaType := "movie"
	if searchItem.Kind == "Series" {
		mediaType = "tv"
	}
	candidate, err := a.scraperSearchTMDB(ctx, settings, mediaType, title, year, item.Season, item.Episode)
	if err != nil {
		return item, "", "", err
	}
	if candidate.ID < 1 {
		return item, "", "", errors.New("TMDB 响应缺少编号")
	}
	item.scraperTMDBID = strconv.Itoa(candidate.ID)
	key, endpoint = a.tmdbIdentity(item)
	if key == "" || endpoint == "" || strings.Contains(endpoint, "/name:") {
		return item, "", "", errors.New("无可识别媒体 TMDB 身份")
	}
	return item, key, endpoint, nil
}

// lockTMDBRequest serializes refreshes for the same TMDB identity while still
// allowing unrelated titles to fetch concurrently. The binary's App state
// contains the same request map and reference counter; this lock lifecycle is
// a source-level reconstruction of that state.
func (a *App) lockTMDBRequest(ctx context.Context, key string) (func(), bool) {
	if ctx == nil {
		ctx = context.Background()
	}
	a.tmdb.requestMu.Lock()
	if a.tmdb.requestRefs == nil {
		a.tmdb.requestRefs = make(map[string]int)
	}
	var gate chan struct{}
	if value, ok := a.tmdb.requests.Load(key); ok {
		gate = value.(chan struct{})
	} else {
		gate = make(chan struct{}, 1)
		a.tmdb.requests.Store(key, gate)
	}
	a.tmdb.requestRefs[key]++
	a.tmdb.requestMu.Unlock()

	forget := func() {
		a.tmdb.requestMu.Lock()
		defer a.tmdb.requestMu.Unlock()
		a.tmdb.requestRefs[key]--
		if a.tmdb.requestRefs[key] <= 0 {
			delete(a.tmdb.requestRefs, key)
			a.tmdb.requests.Delete(key)
		}
	}

	select {
	case gate <- struct{}{}:
		return func() {
			<-gate
			forget()
		}, true
	case <-ctx.Done():
		forget()
		return nil, false
	}
}

// ensureTMDBRequest refreshes the per-item metadata cache. `force` bypasses a
// still-valid cache entry; `includeImages` asks TMDB to include its image
// payload when the caller is resolving artwork. Search and cache behavior are
// reconstructed from the saved Ghidra output and runtime strings.
func (a *App) ensureTMDBRequest(ctx context.Context, item Item, force, includeImages bool) error {
	if ctx == nil {
		ctx = context.Background()
	}
	settings := a.tmdbSettings()
	item, key, endpoint, err := a.resolveTMDBItem(ctx, item, settings)
	if err != nil {
		return err
	}
	if !force {
		if _, ok := readTMDB(key, settings.Directory); ok {
			return nil
		}
	}

	release, ok := a.lockTMDBRequest(ctx, key)
	if !ok {
		return errors.New("TMDB 请求已取消或超时")
	}
	defer release()
	if !force {
		if _, ok := readTMDB(key, settings.Directory); ok {
			return nil
		}
	}

	query := make(url.Values)
	if includeImages {
		query.Set("append_to_response", "images")
	}
	requestEndpoint := tmdbSeriesEndpoint(endpoint)
	var data tmdbData
	if err := a.tmdbGet(ctx, requestEndpoint, query, &data, settings); err != nil {
		return fmt.Errorf("TMDB 元数据请求失败：%w", err)
	}
	if data.ID < 1 {
		parts := strings.Split(requestEndpoint, "/")
		if len(parts) > 1 {
			data.ID, _ = strconv.Atoi(parts[1])
		}
	}
	if data.ID < 1 {
		return errors.New("TMDB 响应缺少编号")
	}
	if data.Title == "" && item.Kind == "Movie" {
		data.Title = item.Name
	}
	if data.Name == "" && item.Kind != "Movie" {
		data.Name = item.Name
	}
	if data.Overview == "" {
		data.Overview = item.Overview
	}
	if data.ReleaseDate == "" && item.Kind == "Movie" {
		data.ReleaseDate = item.PremiereDate
	}
	if data.FirstAirDate == "" && item.Kind == "Series" {
		data.FirstAirDate = item.PremiereDate
	}
	if err := writeTMDB(settings.Directory, tmdbRecord{
		Key:   key,
		Until: time.Now().Add(30 * 24 * time.Hour).Unix(),
		Data:  data,
	}); err != nil {
		return fmt.Errorf("TMDB 缓存保存失败：%w", err)
	}
	return nil
}

func readTMDB(key, directory string) (tmdbRecord, bool) {
	var record tmdbRecord
	if key == "" || directory == "" {
		return record, false
	}

	path := filepath.Join(directory, "tmdb-"+digest(key)+".json")
	data, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(data, &record) != nil {
		return tmdbRecord{}, false
	}
	if record.Key != key || record.Until <= time.Now().Unix() {
		return tmdbRecord{}, false
	}
	return record, true
}

func writeTMDB(directory string, record tmdbRecord) error {
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(directory, ".tmdb-*")
	if err != nil {
		return err
	}
	tempPath := file.Name()
	defer os.Remove(tempPath)

	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	target := filepath.Join(directory, "tmdb-"+digest(record.Key)+".json")
	return os.Rename(tempPath, target)
}

func (a *App) tmdbGet(ctx context.Context, path string, query url.Values, out any, settings tmdbConfig) error {
	// Serialize requests so the configured per-second limit applies to all TMDB
	// callers that share this App.
	a.tmdb.rate.Lock()
	if wait := time.Until(a.tmdb.next); wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			a.tmdb.rate.Unlock()
			return ctx.Err()
		case <-timer.C:
		}
	}
	requestsPerSecond := settings.RequestsPerSecond
	if requestsPerSecond < 1 {
		requestsPerSecond = 15
	}
	a.tmdb.next = time.Now().Add(time.Second / time.Duration(requestsPerSecond))
	a.tmdb.rate.Unlock()

	if query == nil {
		query = make(url.Values)
	}
	language := "zh-CN"
	if ctx != nil {
		if preferred, ok := ctx.Value(scraperLanguageKey{}).(string); ok {
			language = preferred
		}
	}
	query.Set("language", language)
	if settings.APIKey != "" {
		if strings.Contains(settings.APIKey, ".") {
			// TMDB v4 read tokens use Bearer authentication.
		} else {
			query.Set("api_key", settings.APIKey)
		}
	}

	requestURL := strings.TrimRight(settings.APIBase, "/") + "/" + strings.TrimLeft(path, "/")
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return errors.New("TMDB 请求地址无效")
	}
	if strings.Contains(settings.APIKey, ".") {
		request.Header.Set("Authorization", "Bearer "+settings.APIKey)
	}
	request.URL.RawQuery = query.Encode()

	response, err := a.externalHTTPClient("tmdb", http.DefaultClient).Do(request)
	if err != nil {
		return errors.New("TMDB 请求失败或超时")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		if response.StatusCode == http.StatusTooManyRequests {
			a.tmdb.rate.Lock()
			a.tmdb.next = time.Now().Add(time.Second)
			a.tmdb.rate.Unlock()
		}
		return fmt.Errorf("TMDB HTTP %d", response.StatusCode)
	}

	const maxResponseBytes = 2 << 20
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil || len(body) > maxResponseBytes {
		return errors.New("TMDB 响应过大或读取失败")
	}
	if err := json.Unmarshal(body, out); err != nil {
		return errors.New("TMDB 返回了无效 JSON")
	}
	return nil
}

func tmdbArtwork(path string) string {
	if path == "" || path[0] != '/' || strings.Contains(path, "..") || strings.ContainsAny(path, "?#\\") {
		return ""
	}
	return "https://image.tmdb.org/t/p/original" + path
}
