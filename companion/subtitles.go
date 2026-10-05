package main

import (
	"context"
	"errors"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func (a *App) subtitleIdentity(item Item) (Item, string, []string) {
	release := subtitleRelease(item.Path)
	identity := item
	lookup := item
	if item.Kind == "Episode" {
		if parent, err := a.item(item.Parent); err == nil {
			if parent.Kind == "Season" {
				parent, err = a.item(parent.Parent)
			}
			if err == nil && parent.Kind == "Series" {
				lookup = parent
				identity.Name, identity.Year = parent.Name, parent.Year
			}
		}
	}
	metadata := a.metadata(lookup)
	remote := a.cachedTMDB(lookup)
	aliases := []string{subtitleTitle(release), identity.Name, metadata.Title, metadata.OriginalTitle, remote.Title, remote.Name, remote.OriginalTitle, remote.OriginalName}
	return identity, release, aliases
}

func (a *App) prepareSubtitles(item Item) {
	config := a.subtitleSettings()
	if !config.Enabled || item.URL == "" || (item.Kind != "Movie" && item.Kind != "Episode") {
		return
	}
	key := a.subtitleTaskKey(item)
	a.subtitles.mu.Lock()
	if a.subtitles.flights == nil {
		a.subtitles.flights = map[string]*subtitleFlight{}
	}
	if a.subtitles.retry == nil {
		a.subtitles.retry = map[string]time.Time{}
	}
	if len(a.subtitles.flights) >= 8 || a.subtitles.flights[key] != nil || time.Now().Before(a.subtitles.retry[key]) {
		a.subtitles.mu.Unlock()
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	flight := &subtitleFlight{done: make(chan struct{}), cancel: cancel}
	a.subtitles.flights[key] = flight
	a.subtitles.mu.Unlock()
	go func() {
		defer cancel()
		defer func() {
			a.subtitles.mu.Lock()
			if a.subtitles.flights[key] == flight {
				delete(a.subtitles.flights, key)
			}
			if len(a.subtitles.retry) >= 1024 {
				for key, at := range a.subtitles.retry {
					if time.Now().After(at) {
						delete(a.subtitles.retry, key)
					}
				}
			}
			if a.subtitles.retry == nil {
				a.subtitles.retry = map[string]time.Time{}
			}
			a.subtitles.retry[key] = time.Now().Add(time.Minute)
			close(flight.done)
			a.subtitles.mu.Unlock()
		}()
		a.findSubtitle(ctx, item, config)
	}()
}

func (a *App) resolveSubtitles(ctx context.Context, item Item, config subtitleConfig) ([]subtitleMatch, error) {
	key, fingerprint := subtitleKey(item), subtitleFingerprint(item)
	a.subtitles.mu.Lock()
	matches := append([]subtitleMatch(nil), a.subtitles.matches[key]...)
	a.subtitles.mu.Unlock()
	if len(matches) > 0 && matches[0].Fingerprint == fingerprint && time.Now().Before(matches[0].Expires) {
		return matches, nil
	}
	matches = nil
	identity, release, aliases := a.subtitleIdentity(item)
	identity.Kind, identity.Season, identity.Episode = item.Kind, item.Season, item.Episode
	if strings.TrimSpace(release) == "" {
		return nil, subtitleSkip("低可信跳过：缺少视频文件名")
	}
	a.subtitleTaskLog(ctx, item, "字幕搜索："+release)
	candidates, err := a.assrtAPI(ctx, "search", url.Values{"q": {release}, "cnt": {"15"}, "filelist": {"1"}}, config.Token)
	if err != nil {
		return nil, err
	}
	if len(candidates) >= 15 {
		return nil, subtitleSkip("候选冲突：结果未穷尽")
	}
	for _, candidate := range subtitleRankedCandidates(identity, release, aliases, candidates) {
		details, err := a.assrtAPI(ctx, "detail", url.Values{"id": {strconv.Itoa(candidate.ID)}}, config.Token)
		if err != nil {
			return nil, err
		}
		if len(details) != 1 || details[0].ID != candidate.ID {
			continue
		}
		detail := details[0]
		file, ok := subtitleSelectFile(identity, detail)
		score := subtitleScore(identity, release, aliases, detail)
		if !ok || score < 80 || !subtitleOfficialURL(file.URL) {
			continue
		}
		matches = append(matches, subtitleMatch{Detail: detail, File: file, Score: score, Fingerprint: fingerprint, Expires: time.Now().Add(time.Hour)})
	}
	if len(matches) == 0 {
		return nil, nil
	}
	a.subtitles.mu.Lock()
	defer a.subtitles.mu.Unlock()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if a.subtitles.matches == nil || len(a.subtitles.matches) >= 1024 {
		a.subtitles.matches = map[string][]subtitleMatch{}
	}
	a.subtitles.matches[key] = matches
	return matches, nil
}

func (a *App) findSubtitle(ctx context.Context, item Item, config subtitleConfig) {
	ctx, activity := a.subtitleTaskContext(ctx, item)
	var taskErr error
	defer func() { a.finishActivity(activity, taskErr) }()
	source := M{}
	a.enrichMedia(item, a.metadata(item), source)
	if reason := subtitleEmbeddedReason(source["MediaStreams"]); reason != "" {
		a.subtitleTaskLog(ctx, item, reason)
		return
	}
	if _, ok := subtitleRead(item, config); ok {
		a.subtitleTaskLog(ctx, item, "ASSRT · 缓存命中")
		return
	}
	if config.Token == "" {
		a.subtitleTaskLog(ctx, item, "低可信跳过：未配置 ASSRT Token")
		return
	}
	media := a.cachedMedia(item)
	if len(media) == 0 || media["Partial"] == true {
		job := a.queueProbe(item, "", "AIEmby-Subtitle/1.0", false)
		if job == nil {
			a.subtitleTaskLog(ctx, item, "低可信跳过：无法确认内封字幕")
			return
		}
		select {
		case <-ctx.Done():
			taskErr = ctx.Err()
			return
		case <-job.done:
		}
		if job.err != nil || len(job.data) == 0 || job.data["Partial"] == true {
			a.subtitleTaskLog(ctx, item, "低可信跳过：无法确认内封字幕")
			return
		}
		media = job.data
	}
	if len(subtitleStreams(media["MediaStreams"])) == 0 {
		a.subtitleTaskLog(ctx, item, "低可信跳过：媒体信息为空")
		return
	}
	if reason := subtitleEmbeddedReason(media["MediaStreams"]); reason != "" {
		a.subtitleTaskLog(ctx, item, reason)
		return
	}
	matches, err := a.resolveSubtitles(ctx, item, config)
	if err != nil {
		var skipped subtitleSkip
		if !errors.As(err, &skipped) {
			taskErr = err
		}
		a.subtitleTaskLog(ctx, item, err.Error())
		return
	}
	for _, match := range matches {
		data, err := a.subtitleRequest(ctx, match.File.URL, "", subtitleMaxBytes)
		if err != nil {
			taskErr = err
			continue
		}
		content, name, ext, language, ok := subtitlePayload(item, match.File.Name, data)
		if !ok {
			continue
		}
		a.subtitles.storage.Lock()
		current, readErr := a.item(item.ID)
		settings := a.subtitleSettings()
		if ctx.Err() != nil || readErr != nil || subtitleFingerprint(current) != subtitleFingerprint(item) || settings != config {
			a.subtitles.storage.Unlock()
			taskErr = context.Canceled
			return
		}
		err = subtitleWriteNamed(item, config, content, ext, name, language, match.Score)
		cached := err == nil
		if err == nil && config.SaveBesideMedia {
			if record, ok := subtitleReadCache(item, config); ok {
				_, err = subtitleWriteMedia(item, config, record)
			}
		}
		a.subtitles.storage.Unlock()
		if err != nil {
			taskErr = err
			if cached {
				a.subtitleTaskLog(ctx, item, "字幕已缓存，可继续播放；保存到媒体目录失败")
				return
			}
			continue
		}
		taskErr = nil
		a.subtitleTaskLog(ctx, item, "ASSRT · "+name+" · 下载成功")
		return
	}
	if taskErr != nil {
		a.subtitleTaskLog(ctx, item, "字幕下载失败")
	} else {
		a.subtitleTaskLog(ctx, item, "跳过：未找到可信中文字幕")
	}
}

func (a *App) cachedSubtitleIndex(item Item) int {
	media := M{}
	a.enrichMedia(item, a.metadata(item), media)
	index := -1
	for _, stream := range subtitleStreams(media["MediaStreams"]) {
		index = max(index, int(catalogDTOFloat(stream["Index"])))
	}
	return index + 1
}

func (a *App) appendSubtitles(item Item, source M, r *http.Request, user User) {
	if user.API || user.Max <= 0 {
		return
	}
	config := a.subtitleSettings()
	if !config.Enabled {
		return
	}
	record, ok := subtitleRead(item, config)
	if !ok {
		return
	}
	streams := subtitleStreams(source["MediaStreams"])
	for _, stream := range streams {
		if stream["Id"] == record.ID {
			return
		}
	}
	index := -1
	for _, stream := range streams {
		index = max(index, int(catalogDTOFloat(stream["Index"])))
	}
	index++
	ext := filepath.Ext(record.Path)
	address := "/Videos/" + item.ID + "/Subtitles/" + record.ID + "/Stream" + ext + "?api_key=" + url.QueryEscape(token(r))
	streams = append(streams, M{"Type": "Subtitle", "Language": "zho", "DisplayTitle": record.LanguageName, "Title": record.LanguageName, "IsExternal": true, "IsTextSubtitleStream": true, "SupportsExternalStream": true, "Codec": strings.TrimPrefix(ext, "."), "Index": index, "Id": record.ID, "DeliveryMethod": "External", "DeliveryUrl": address, "ExternalUrl": address, "StreamUrl": address, "DeliveryFormat": strings.TrimPrefix(ext, "."), "Path": address, "MediaSourceId": item.ID, "ItemId": item.ID, "ExternalStreamIndex": index, "IsHearingImpaired": false, "IsDefault": false, "IsForced": false})
	source["MediaStreams"] = streams
}

func subtitleRouteParts(path string) ([]string, bool) {
	parts := strings.Split(strings.Trim(embyPath(path), "/"), "/")
	if (len(parts) != 5 && len(parts) != 6) || !strings.EqualFold(parts[0], "Videos") {
		return nil, false
	}
	position := 2
	if len(parts) == 6 {
		position = 3
	}
	if !strings.EqualFold(parts[position], "Subtitles") {
		return nil, false
	}
	if len(parts) == 5 {
		if !regexpSubtitleID(parts[3]) {
			return nil, false
		}
	} else {
		if index, err := strconv.Atoi(parts[4]); err != nil || index < 0 {
			return nil, false
		}
	}
	stream := strings.ToLower(parts[len(parts)-1])
	if stream != "stream.srt" && stream != "stream.ass" && stream != "stream.ssa" && stream != "stream.vtt" {
		return nil, false
	}
	return parts, true
}

func regexpSubtitleID(value string) bool {
	if len(value) != 21 || !strings.HasPrefix(value, "auto-") {
		return false
	}
	for _, char := range value[5:] {
		if !(char >= '0' && char <= '9' || char >= 'a' && char <= 'f') {
			return false
		}
	}
	return true
}

func (a *App) serveSubtitle(w http.ResponseWriter, r *http.Request, user User, parts []string) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		fail(w, 405, "GET or HEAD required")
		return
	}
	if user.API || user.ID == "" || user.Max <= 0 {
		fail(w, 403, "无播放权限")
		return
	}
	itemID := parts[1]
	if len(parts) == 6 {
		itemID = parts[2]
	}
	item, err := a.itemForUser(r, itemID)
	if err != nil {
		fail(w, 404, "字幕不存在")
		return
	}
	if len(parts) == 6 && canonicalPlaybackID(parts[1]) != item.ID {
		selected, err := a.itemForUser(r, parts[1])
		if err != nil {
			fail(w, 404, "字幕不存在")
			return
		}
		versions := a.versionSources(selected, r, user, false)
		found := false
		for _, source := range versions {
			if source["ItemId"] == item.ID {
				found = true
			}
		}
		if !found {
			fail(w, 404, "字幕不存在")
			return
		}
	}
	config := a.subtitleSettings()
	if !config.Enabled {
		fail(w, 404, "字幕不存在")
		return
	}
	record, ok := subtitleRead(item, config)
	if !ok {
		fail(w, 404, "字幕不存在")
		return
	}
	if len(parts) == 5 && parts[3] != record.ID {
		fail(w, 404, "字幕不存在")
		return
	}
	if len(parts) == 6 {
		index, _ := strconv.Atoi(parts[4])
		if index != a.cachedSubtitleIndex(item) {
			fail(w, 404, "字幕不存在")
			return
		}
	}
	ext := filepath.Ext(record.Path)
	if ext != strings.ToLower(filepath.Ext(parts[len(parts)-1])) {
		fail(w, 404, "字幕格式不存在")
		return
	}
	directory, path := config.Directory, record.Path
	if filepath.IsAbs(path) {
		directory, path = filepath.Dir(path), filepath.Base(path)
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		fail(w, 404, "字幕不存在")
		return
	}
	defer root.Close()
	data, err := subtitleReadLimited(root, path, subtitleMaxBytes)
	if err != nil {
		fail(w, 404, "字幕不存在")
		return
	}
	contentType := "text/plain; charset=utf-8"
	if ext == ".vtt" {
		contentType = "text/vtt; charset=utf-8"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", mime.FormatMediaType("inline", map[string]string{"filename": filepath.Base(record.FinalName)}))
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	if r.Method == http.MethodGet {
		_, _ = w.Write(data)
	}
}

func (a *App) subtitleSession(user User, itemID, session string, stopped bool) {
	if user.API || itemID == "" {
		return
	}
	item, err := a.item(canonicalPlaybackID(itemID))
	if err != nil {
		return
	}
	config := a.subtitleSettings()
	if !config.Enabled {
		return
	}
	key := user.ID + "|" + user.Device + "|" + session
	a.subtitles.storage.Lock()
	defer a.subtitles.storage.Unlock()
	a.subtitles.mu.Lock()
	if a.subtitles.sessions == nil {
		a.subtitles.sessions = map[string]map[string]time.Time{}
	}
	now := time.Now()
	for id, members := range a.subtitles.sessions {
		for member, updated := range members {
			if now.Sub(updated) > 3*time.Minute {
				delete(members, member)
			}
		}
		if len(members) == 0 {
			delete(a.subtitles.sessions, id)
		}
	}
	if stopped {
		for member := range a.subtitles.sessions[item.ID] {
			if member == key || session == "" && strings.HasPrefix(member, user.ID+"|"+user.Device+"|") {
				delete(a.subtitles.sessions[item.ID], member)
			}
		}
		if len(a.subtitles.sessions[item.ID]) == 0 {
			delete(a.subtitles.sessions, item.ID)
		}
	} else {
		if a.subtitles.sessions[item.ID] == nil {
			a.subtitles.sessions[item.ID] = map[string]time.Time{}
		}
		a.subtitles.sessions[item.ID][key] = now
	}
	active := len(a.subtitles.sessions[item.ID]) > 0
	a.subtitles.mu.Unlock()
	if !stopped {
		a.prepareSubtitles(item)
		return
	}
	if active || !config.AutoClean {
		return
	}
	var count int
	if a.db.QueryRow("SELECT count(*) FROM plays WHERE item=? AND updated>?", item.ID, time.Now().Add(-3*time.Minute).Unix()).Scan(&count) != nil || count != 0 {
		return
	}
	a.subtitles.mu.Lock()
	if flight := a.subtitles.flights[a.subtitleTaskKey(item)]; flight != nil {
		flight.cancel()
	}
	delete(a.subtitles.matches, subtitleKey(item))
	delete(a.subtitles.retry, a.subtitleTaskKey(item))
	a.subtitles.mu.Unlock()
	if err := subtitleRemove(item, config); err != nil {
		ctx, activity := a.subtitleTaskContext(context.Background(), item)
		a.subtitleTaskLog(ctx, item, "字幕缓存清理失败")
		a.finishActivity(activity, err)
	}
}
