package main

import (
	"encoding/json"
	"fmt"
	"html"
	"log"
	"math"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type NotifyEvent struct {
	Type         string
	Action       string
	UserName     string
	ItemID       string
	ItemName     string
	MediaType    string
	Year         int
	Rating       float64
	Progress     float64
	IP           string
	Client       string
	Device       string
	TMDBID       string
	IMDBID       string
	Overview     string
	ImageURL     string
	Time         time.Time
	SessionID    string
	UserID       string
	SeriesName   string
	EpisodeCount int
}

type episodeBatch struct {
	event    NotifyEvent
	deadline time.Time
	seen     map[string]bool
}

type notificationJob struct {
	app   *App
	event NotifyEvent
}

func (a *App) notify(event NotifyEvent) {
	if event.Action != "stop" && !telegramEventEnabled(a.telegramSettings(), event) {
		return
	}
	if event.Time.IsZero() {
		event.Time = time.Now()
	}
	a.telegram.mu.Lock()
	defer a.telegram.mu.Unlock()
	if event.Type == "playback" {
		key := event.UserID + "|" + event.SessionID + "|" + event.ItemID
		if a.telegram.sessions == nil {
			a.telegram.sessions = map[string]time.Time{}
		}
		if event.Action == "stop" {
			delete(a.telegram.sessions, key)
			return
		}
		previous, exists := a.telegram.sessions[key]
		a.telegram.sessions[key] = event.Time
		for member, updated := range a.telegram.sessions {
			if event.Time.Sub(updated) > 24*time.Hour {
				delete(a.telegram.sessions, member)
			}
		}
		if exists && event.Time.Sub(previous) < 24*time.Hour {
			return
		}
	}
	if len(a.telegram.queue) >= 1024 {
		return
	}
	a.telegram.queue = append(a.telegram.queue, notificationJob{app: a, event: event})
	if !a.telegram.running {
		a.telegram.running = true
		a.telegram.wake = make(chan struct{}, 1)
		go a.drainNotifications()
	}
	select {
	case a.telegram.wake <- struct{}{}:
	default:
	}
}

func (a *App) drainNotifications() {
	groups := map[string]*episodeBatch{}
	for {
		for key, group := range groups {
			if !time.Now().Before(group.deadline) {
				delete(groups, key)
				a.sendNotification(group.event)
			}
		}
		a.telegram.mu.Lock()
		var job notificationJob
		if len(a.telegram.queue) > 0 {
			job = a.telegram.queue[0]
			a.telegram.queue[0] = notificationJob{}
			a.telegram.queue = a.telegram.queue[1:]
		}
		debounce := time.Duration(a.telegram.debounce)
		if debounce <= 0 {
			debounce = time.Minute
		}
		wake := a.telegram.wake
		if job.app == nil && len(groups) == 0 {
			a.telegram.running = false
			a.telegram.queue = nil
			a.telegram.mu.Unlock()
			return
		}
		a.telegram.mu.Unlock()
		if job.app != nil {
			event := job.event
			seriesID := ""
			if event.Type == "new-media" && telegramEventEnabled(job.app.telegramSettings(), event) {
				if item, err := job.app.item(event.ItemID); err == nil && item.Kind == "Episode" {
					seriesID = job.app.resumeSeriesID(item)
				}
			}
			if seriesID == "" {
				job.app.sendNotification(event)
				continue
			}
			series, err := job.app.item(seriesID)
			if err != nil || series.Kind != "Series" {
				job.app.sendNotification(event)
				continue
			}
			group := groups[seriesID]
			if group != nil && !time.Now().Before(group.deadline) {
				delete(groups, seriesID)
				job.app.sendNotification(group.event)
				group = nil
			}
			if group == nil {
				if len(groups) >= 1024 {
					job.app.sendNotification(event)
					continue
				}
				event.SeriesName = series.Name
				event.EpisodeCount = 0
				group = &episodeBatch{event: event, deadline: time.Now().Add(debounce), seen: map[string]bool{}}
				groups[seriesID] = group
			}
			if !group.seen[event.ItemID] && len(group.seen) >= 1024 {
				delete(groups, seriesID)
				job.app.sendNotification(group.event)
				job.app.sendNotification(event)
				continue
			}
			if !group.seen[event.ItemID] {
				group.seen[event.ItemID] = true
				group.event.EpisodeCount++
			}
			continue
		}
		var next time.Time
		for key, group := range groups {
			if !time.Now().Before(group.deadline) {
				delete(groups, key)
				a.sendNotification(group.event)
			} else if next.IsZero() || group.deadline.Before(next) {
				next = group.deadline
			}
		}
		if next.IsZero() {
			continue
		}
		timer := time.NewTimer(max(time.Duration(0), time.Until(next)))
		select {
		case <-wake:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		case <-timer.C:
		}
	}
}

func (a *App) sendNotification(event NotifyEvent) {
	if !telegramEventEnabled(a.telegramSettings(), event) {
		return
	}
	a.enrichNotification(&event)
	if err := a.sendTelegram(event); err != nil {
		a.telegramLog("Telegram 通知失败", err.Error())
	} else {
		a.telegramLog("Telegram 通知 · "+event.Type, "")
	}
}

func (a *App) telegramLog(message, detail string) {
	if token := a.telegramSettings().Token; token != "" {
		message, detail = strings.ReplaceAll(message, token, "[隐藏]"), strings.ReplaceAll(detail, token, "[隐藏]")
	}
	if detail == "" {
		log.Print(message)
	} else {
		log.Print(message, ": ", detail)
	}
	key := a.newActivity("telegram", "", message)
	a.changeActivity(key, func(entry *activityEntry) {
		entry.State = "complete"
		if detail != "" {
			entry.State, entry.Error = "error", detail
		}
	})
}

func (a *App) enrichNotification(event *NotifyEvent) {
	item, err := a.item(event.ItemID)
	if err != nil {
		return
	}
	event.ItemName, event.MediaType, event.Year = item.Name, item.Kind, item.Year
	event.Overview, event.ImageURL = item.Overview, item.Poster
	var raw string
	var metadata sidecar
	if a.db.QueryRow("SELECT data FROM item_metadata WHERE item=?", item.ID).Scan(&raw) == nil {
		_ = json.Unmarshal([]byte(raw), &metadata)
	}
	event.Rating, event.TMDBID, event.IMDBID = metadata.Rating, metadata.TMDB, metadata.IMDB
	if event.Overview == "" {
		event.Overview = metadata.Plot
	}
	for _, provider := range metadata.UniqueIDs {
		switch strings.ToLower(provider.Type) {
		case "tmdb":
			if event.TMDBID == "" {
				event.TMDBID = provider.Value
			}
		case "imdb":
			if event.IMDBID == "" {
				event.IMDBID = provider.Value
			}
		}
	}
	remote := a.cachedTMDB(item)
	if event.Rating == 0 {
		event.Rating = remote.Rating
	}
	if event.Overview == "" {
		event.Overview = remote.Overview
	}
	if event.TMDBID == "" && remote.ID > 0 && item.Kind != "Episode" {
		event.TMDBID = strconv.Itoa(remote.ID)
	}
	if event.ImageURL == "" && remote.Poster != "" {
		event.ImageURL = "https://image.tmdb.org/t/p/w500" + remote.Poster
	}
	if item.Kind == "Episode" {
		if series := a.resumeSeriesID(item); series != "" && series != item.ID {
			seriesItem, err := a.item(series)
			if err == nil && seriesItem.Kind == "Series" {
				parent := NotifyEvent{ItemID: series}
				a.enrichNotification(&parent)
				if event.TMDBID == "" {
					event.TMDBID = parent.TMDBID
				}
				if event.IMDBID == "" {
					event.IMDBID = parent.IMDBID
				}
				if event.ImageURL == "" {
					event.ImageURL = parent.ImageURL
				}
			}
		}
	}
	var cover int
	if a.db.QueryRow("SELECT 1 FROM covers WHERE id=?", item.ID).Scan(&cover) == nil {
		event.ImageURL = "cover:" + item.ID
	}
}

func (a *App) notifyPlayback(r *http.Request, user User, itemID, session string, position, duration int64, stopped bool) {
	if user.API || strings.EqualFold(q(r, "GoEmbyProbe"), "true") || strings.EqualFold(q(r, "AIEmbyProbe"), "true") {
		return
	}
	if session == "" {
		session = user.Device
	}
	client, device := field(r, "Client"), field(r, "Device")
	if client == "" {
		client = r.Header.Get("X-Emby-Client")
	}
	if client == "" {
		client = r.UserAgent()
	}
	if device == "" {
		device = r.Header.Get("X-Emby-Device-Name")
	}
	if device == "" {
		device = user.Device
	}
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		ip = r.RemoteAddr
	}
	if peer := net.ParseIP(ip); peer != nil && (peer.IsLoopback() || ip == "172.18.0.1") {
		forwarded := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
		candidate := strings.TrimSpace(forwarded[len(forwarded)-1])
		if net.ParseIP(candidate) != nil {
			ip = candidate
		}
	}
	progress := float64(0)
	if duration > 0 {
		progress = math.Min(100, math.Max(0, float64(position)*100/float64(duration)))
	}
	action := "start"
	if stopped {
		action = "stop"
	}
	a.notify(NotifyEvent{Type: "playback", Action: action, UserID: user.ID, UserName: user.Name, ItemID: itemID, SessionID: session, Progress: progress, IP: ip, Client: client, Device: device})
}

func telegramRating(rating float64, kind string) string {
	if rating > 0 {
		for _, grade := range []struct {
			minimum float64
			label   string
		}{{9.8, "👑 口碑极佳"}, {9.5, "💥 高分佳作"}, {9, "🤙 广受好评"}, {8.5, "👍 值得一看"}, {8, "🍺 表现不错"}, {7.5, "🤏 评价尚可"}, {7, "🙄 评价一般"}, {6, "😅 评价分歧"}, {5, "💩 评分较低"}, {4, "🤮 口碑不佳"}, {0, "👻 暂不推荐"}} {
			if rating >= grade.minimum {
				return grade.label
			}
		}
	}
	if kind == "Movie" {
		return "🎬 新电影已入库"
	}
	if kind == "Series" || kind == "Season" || kind == "Episode" {
		return "📺 新剧集已入库"
	}
	return "🆕 新媒体已入库"
}

func telegramText(text string, limit int) string {
	runes := []rune(text)
	if len(runes) > limit {
		text = string(runes[:limit]) + "…"
	}
	return html.EscapeString(text)
}

func telegramMessage(event NotifyEvent) (string, M) {
	kind, icon, providerType := "其他", "🆕", "tv"
	switch event.MediaType {
	case "Movie":
		kind, icon, providerType = "电影", "🎬", "movie"
	case "Series", "Season", "Episode":
		kind, icon = "剧集", "📺"
	}
	text := icon + " 【AI Emby】新入库 " + kind + " " + telegramText(event.ItemName, 32)
	if event.Year > 0 {
		text += fmt.Sprintf(" (%d)", event.Year)
	}
	batch := event.Type == "new-media" && event.MediaType == "Episode" && event.EpisodeCount > 1
	if batch {
		text = "📺 【AI Emby】剧集更新 " + telegramText(event.SeriesName, 32)
	}
	if event.Type == "playback" {
		text = "▶️ 【" + telegramText(event.UserName, 32) + "】开始播放 " + kind + " " + telegramText(event.ItemName, 32)
	}
	rating := "暂无"
	if event.Rating > 0 {
		rating = fmt.Sprintf("%.1f/10", event.Rating)
	}
	text += "\n⭐️ 评分：" + rating + "\n🎦 媒体类型：" + kind
	if batch {
		text += fmt.Sprintf("\n本次更新共 %d 集", event.EpisodeCount)
	}
	if event.Type == "playback" {
		text += fmt.Sprintf("\n🔄 播放进度：%.1f%%\n🌐 来源 IP：%s\n📱 %s / %s\n👤 用户：%s", event.Progress, telegramText(event.IP, 32), telegramText(event.Client, 32), telegramText(event.Device, 32), telegramText(event.UserName, 32))
	}
	text += "\n🍿 TMDB ID：" + telegramText(event.TMDBID, 32) + "\n🌟 IMDb ID：" + telegramText(event.IMDBID, 32) + "\n🕒 操作时间：" + event.Time.Format("2006-01-02 15:04:05") + "\n📝 简介：" + telegramText(event.Overview, 100)
	if event.Type == "new-media" {
		text += "\n\n" + telegramRating(event.Rating, event.MediaType)
	}
	buttons := []M{}
	if event.TMDBID != "" {
		buttons = append(buttons, M{"text": "TMDB", "url": "https://www.themoviedb.org/" + providerType + "/" + url.PathEscape(event.TMDBID)})
	}
	search := event.ItemName
	if event.IMDBID != "" {
		search = event.IMDBID
	}
	buttons = append(buttons, M{"text": "豆瓣", "url": "https://www.douban.com/search?q=" + url.QueryEscape(search)})
	if event.IMDBID != "" {
		buttons = append(buttons, M{"text": "IMDb", "url": "https://www.imdb.com/title/" + url.PathEscape(event.IMDBID) + "/"})
	}
	return text, M{"inline_keyboard": [][]M{buttons}}
}
