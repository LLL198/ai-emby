package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"sort"
	"time"
)

const playbackTicksPerSecond int64 = 10000000

func defaultIntroConfig() introConfig {
	return introConfig{AutoIntro: true, AutoCredits: true, Directory: filepath.Join(introDataRoot(), "intro-credits"), WindowMinutes: 10, MinSamples: 3, ToleranceSeconds: 15}
}

func validIntroConfig(config introConfig) bool {
	return config.WindowMinutes >= 1 && config.WindowMinutes <= 30 && config.MinSamples >= 2 && config.MinSamples <= 20 && config.ToleranceSeconds >= 1 && config.ToleranceSeconds <= 120
}

func introMarkerKey(parent string, season int) string {
	return fmt.Sprintf("%s:%d", parent, season)
}

func (a *App) storeIntroMarker(parent string, marker introMarker) {
	key := introMarkerKey(parent, marker.Season)
	if _, exists := a.intro.markers[key]; !exists && len(a.intro.markers) >= introRecordLimit {
		a.intro.markerOverflow = true
		for member := range a.intro.markers {
			delete(a.intro.markers, member)
			break
		}
	}
	a.intro.markers[key] = marker
}

func (a *App) loadIntroSettings() {
	config := defaultIntroConfig()
	var raw string
	if a.db.QueryRow("SELECT v FROM settings WHERE k='intro_config'").Scan(&raw) == nil {
		if err := json.Unmarshal([]byte(raw), &config); err != nil || !validIntroConfig(config) {
			config = defaultIntroConfig()
		}
	}
	if config.Directory == "" {
		config.Directory = defaultIntroConfig().Directory
	}
	a.intro.mu.Lock()
	a.intro.ctx, a.intro.cancel = context.WithCancel(context.Background())
	a.intro.config = config
	a.intro.sessions = map[string]introPlayback{}
	a.intro.active = map[string]string{}
	a.intro.groups = map[string]*introGroup{}
	a.intro.markers = map[string]introMarker{}
	a.intro.queue = make(chan introCandidate, 128)
	a.intro.recordQueue = make(chan introRecordWrite, 128)
	a.intro.learningDone = make(chan struct{})
	a.intro.mu.Unlock()
	rows, err := a.db.Query("SELECT series_id,season,parent_id,intro_start_ticks,intro_end_ticks,credits_start_ticks,intro_samples,credits_samples,source FROM intro_markers ORDER BY updated_at DESC LIMIT ?", introRecordLimit+1)
	if err == nil {
		for rows.Next() {
			var parent string
			var marker introMarker
			if err = rows.Scan(&marker.SeriesID, &marker.Season, &parent, &marker.IntroStartTicks, &marker.IntroEndTicks, &marker.CreditsStartTicks, &marker.IntroSamples, &marker.CreditsSamples, &marker.Source); err != nil {
				break
			}
			a.intro.mu.Lock()
			if len(a.intro.markers) >= introRecordLimit {
				a.intro.markerOverflow = true
				a.intro.mu.Unlock()
				break
			}
			a.storeIntroMarker(parent, marker)
			a.intro.mu.Unlock()
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
	}
	if err != nil {
		a.introLog("", "读取片头片尾标记失败", err)
	}
	a.intro.enabled.Store(config.Enabled)
	a.intro.workers.Add(3)
	go func() { defer a.intro.workers.Done(); a.introWorker() }()
	go func() { defer a.intro.workers.Done(); a.introRecordWorker() }()
	go func() {
		defer a.intro.workers.Done()
		a.loadIntroRecords(config.Directory, 0)
		a.seedIntroRecordFiles(config.Directory, 0)
	}()
}

func (a *App) stopIntroLearning() {
	a.intro.mu.Lock()
	if a.intro.cancel != nil {
		a.intro.enabled.Store(false)
		a.intro.cancel()
	}
	a.intro.mu.Unlock()
	a.intro.workers.Wait()
}

func (a *App) introSettingsAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		a.intro.mu.Lock()
		config := a.intro.config
		a.intro.mu.Unlock()
		respond(w, config)
		return
	}
	if r.Method != http.MethodPut {
		fail(w, 405, "PUT required")
		return
	}
	var config introConfig
	if !body(w, r, &config) {
		return
	}
	if !validIntroConfig(config) {
		fail(w, 400, "片头片尾参数超出范围")
		return
	}
	directory, err := validateIntroDirectory(config.Directory)
	if err != nil {
		fail(w, 400, "片头片尾数据目录必须位于数据根目录且可写")
		return
	}
	config.Directory = directory
	a.intro.persistMu.Lock()
	a.intro.mu.Lock()
	previous := a.intro.config
	a.intro.mu.Unlock()
	if previous.Directory != directory {
		err = migrateIntroDirectory(previous.Directory, directory)
	}
	if err == nil {
		var data []byte
		data, err = json.Marshal(config)
		if err == nil {
			_, err = a.db.Exec("INSERT INTO settings(k,v) VALUES('intro_config',?) ON CONFLICT(k) DO UPDATE SET v=excluded.v", string(data))
		}
	}
	if err != nil {
		a.intro.persistMu.Unlock()
		fail(w, 500, "保存片头片尾设置失败")
		return
	}
	a.intro.mu.Lock()
	a.intro.config = config
	a.intro.generation++
	generation := a.intro.generation
	a.intro.sessions = map[string]introPlayback{}
	a.intro.active = map[string]string{}
	a.intro.groups = map[string]*introGroup{}
	a.intro.ring = [1024]introSessionSlot{}
	a.intro.next = 0
	a.intro.enabled.Store(config.Enabled)
	a.intro.mu.Unlock()
	a.intro.persistMu.Unlock()
	if previous.Directory != directory {
		a.intro.mu.Lock()
		if a.intro.ctx.Err() == nil {
			a.intro.workers.Add(1)
			go func() {
				defer a.intro.workers.Done()
				a.loadIntroRecords(directory, generation)
				a.seedIntroRecordFiles(directory, generation)
			}()
		}
		a.intro.mu.Unlock()
	}
	respond(w, config)
}

func (a *App) clearIntroRecords(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		fail(w, 405, "DELETE required")
		return
	}
	a.intro.persistMu.Lock()
	defer a.intro.persistMu.Unlock()
	a.intro.mu.Lock()
	directory := a.intro.config.Directory
	a.intro.generation++
	a.intro.mu.Unlock()
	if _, err := a.db.Exec("DELETE FROM intro_markers"); err != nil {
		fail(w, 500, "清除失败")
		return
	}
	a.intro.mu.Lock()
	a.intro.sessions = map[string]introPlayback{}
	a.intro.active = map[string]string{}
	a.intro.groups = map[string]*introGroup{}
	a.intro.markers = map[string]introMarker{}
	a.intro.ring = [1024]introSessionSlot{}
	a.intro.next = 0
	a.intro.markerOverflow = false
	a.intro.mu.Unlock()
	if err := clearIntroRecordFiles(directory); err != nil {
		a.introLog("", "清除片头片尾文件失败", err)
		fail(w, 500, "清除片头片尾文件失败")
		return
	}
	a.introLog("", "学习记录已清除", nil)
	respond(w, M{"ok": true})
}

func (a *App) recordIntroEvent(user User, itemID, session, route string, position *int64, duration int64) {
	if !a.intro.enabled.Load() || user.API {
		return
	}
	now := time.Now()
	device := user.ID + "|" + user.Device
	key := device + "|" + session + "|" + itemID
	a.intro.mu.Lock()
	defer a.intro.mu.Unlock()
	config := a.intro.config
	if !config.Enabled {
		return
	}
	state, exists := a.intro.sessions[key]
	enqueue := func(state introPlayback, credits bool, seconds int64) {
		select {
		case a.intro.queue <- introCandidate{item: state.item, seconds: seconds, credits: credits, generation: a.intro.generation}:
		default:
		}
	}
	finish := func(state introPlayback) {
		remaining := state.duration - state.last
		if config.AutoCredits && state.last > 0 && remaining >= 15*playbackTicksPerSecond && remaining <= 300*playbackTicksPerSecond {
			enqueue(state, true, state.last/playbackTicksPerSecond)
		}
	}
	if !exists && position != nil {
		if previous := a.intro.active[device]; previous != "" && previous != key {
			finish(a.intro.sessions[previous])
			delete(a.intro.sessions, previous)
		}
		a.intro.active[device] = key
	}
	if route == "/sessions/playing/stopped" {
		if position != nil {
			state.last = *position
		}
		if duration > 0 {
			state.duration = duration
		}
		state.item = itemID
		finish(state)
		delete(a.intro.sessions, key)
		if a.intro.active[device] == key {
			delete(a.intro.active, device)
		}
		return
	}
	if position == nil || *position < 0 {
		return
	}
	if !exists {
		a.intro.next++
		slot := &a.intro.ring[a.intro.next%uint64(len(a.intro.ring))]
		if previous, exists := a.intro.sessions[slot.key]; exists && previous.seq == slot.seq {
			delete(a.intro.sessions, slot.key)
			if a.intro.active[slot.device] == slot.key {
				delete(a.intro.active, slot.device)
			}
		}
		*slot = introSessionSlot{key: key, device: device, seq: a.intro.next}
		a.intro.sessions[key] = introPlayback{item: itemID, seq: a.intro.next, last: *position, duration: duration, updated: now}
		return
	}
	if duration > 0 {
		state.duration = duration
	}
	jump := *position - state.last
	if config.AutoIntro && !state.pending && state.last <= 20*playbackTicksPerSecond && jump > 20*playbackTicksPerSecond && *position <= int64(config.WindowMinutes)*60*playbackTicksPerSecond {
		state.candidate, state.pending = *position/playbackTicksPerSecond, true
	}
	if state.pending && jump >= 5*playbackTicksPerSecond && jump <= 45*playbackTicksPerSecond && *position >= (state.candidate+5)*playbackTicksPerSecond {
		enqueue(state, false, state.candidate)
		state.pending = false
	}
	remaining := state.duration - *position
	if config.AutoCredits && state.duration > 0 && jump > 60*playbackTicksPerSecond && remaining >= 15*playbackTicksPerSecond && remaining <= 300*playbackTicksPerSecond {
		enqueue(state, true, *position/playbackTicksPerSecond)
	}
	state.last, state.updated = *position, now
	a.intro.sessions[key] = state
}

func (a *App) introWorker() {
	defer close(a.intro.learningDone)
	process := func(candidate introCandidate) {
		if err := a.aggregateIntroCandidate(candidate); err != nil {
			a.introLog(candidate.item, "保存标记失败", err)
		}
	}
	for {
		select {
		case candidate := <-a.intro.queue:
			process(candidate)
		case <-a.intro.ctx.Done():
			for {
				select {
				case candidate := <-a.intro.queue:
					process(candidate)
				default:
					return
				}
			}
		}
	}
}

func (a *App) aggregateIntroCandidate(candidate introCandidate) error {
	item, err := a.item(candidate.item)
	if err != nil {
		return err
	}
	if item.Kind != "Episode" || candidate.seconds <= 0 {
		return nil
	}
	series, err := a.item(a.resumeSeriesID(item))
	if err != nil || series.Kind != "Series" {
		return err
	}
	key := introMarkerKey(series.ID, item.Season)
	a.intro.mu.Lock()
	config := a.intro.config
	if !config.Enabled || candidate.generation != a.intro.generation || (candidate.credits && !config.AutoCredits) || (!candidate.credits && !config.AutoIntro) {
		a.intro.mu.Unlock()
		return nil
	}
	group := a.intro.groups[key]
	if group == nil {
		if len(a.intro.groups) >= 512 {
			for member := range a.intro.groups {
				delete(a.intro.groups, member)
				break
			}
		}
		group = &introGroup{intro: map[string]int64{}, credits: map[string]int64{}}
		a.intro.groups[key] = group
	}
	samples := group.intro
	label := "片头"
	if candidate.credits {
		samples, label = group.credits, "片尾"
	}
	if _, exists := samples[item.ID]; exists || len(samples) >= 256 {
		a.intro.mu.Unlock()
		return nil
	}
	samples[item.ID] = candidate.seconds
	values := make([]int64, 0, len(samples))
	for _, value := range samples {
		values = append(values, value)
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	median, matches := values[len(values)/2], 0
	for _, value := range values {
		if value >= median-int64(config.ToleranceSeconds) && value <= median+int64(config.ToleranceSeconds) {
			matches++
		}
	}
	a.intro.mu.Unlock()
	a.introLog(item.ID, fmt.Sprintf("%s · S%02dE%02d · %s候选：%02d:%02d · 样本 %d/%d", series.Name, item.Season, item.Episode, label, candidate.seconds/60, candidate.seconds%60, matches, config.MinSamples), nil)
	if matches < config.MinSamples {
		return nil
	}
	a.intro.persistMu.Lock()
	defer a.intro.persistMu.Unlock()
	a.intro.mu.Lock()
	current := candidate.generation == a.intro.generation && a.intro.config.Enabled
	a.intro.mu.Unlock()
	if !current {
		return nil
	}
	marker := introMarker{SeriesID: series.ID, Season: item.Season, Source: "behavior"}
	err = a.db.QueryRow("SELECT intro_start_ticks,intro_end_ticks,credits_start_ticks,intro_samples,credits_samples,source FROM intro_markers WHERE series_id=? AND season=?", series.ID, item.Season).Scan(&marker.IntroStartTicks, &marker.IntroEndTicks, &marker.CreditsStartTicks, &marker.IntroSamples, &marker.CreditsSamples, &marker.Source)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if candidate.credits {
		if marker.CreditsStartTicks > 0 {
			return nil
		}
		marker.CreditsStartTicks, marker.CreditsSamples = median*playbackTicksPerSecond, matches
	} else {
		if marker.IntroEndTicks > 0 {
			return nil
		}
		marker.IntroEndTicks, marker.IntroSamples = median*playbackTicksPerSecond, matches
	}
	updated := time.Now()
	_, err = a.db.Exec("INSERT INTO intro_markers(series_id,season,parent_id,intro_start_ticks,intro_end_ticks,credits_start_ticks,intro_samples,credits_samples,sample_count,confidence,source,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(series_id,season) DO UPDATE SET parent_id=excluded.parent_id,intro_start_ticks=excluded.intro_start_ticks,intro_end_ticks=excluded.intro_end_ticks,credits_start_ticks=excluded.credits_start_ticks,intro_samples=excluded.intro_samples,credits_samples=excluded.credits_samples,sample_count=excluded.sample_count,confidence=excluded.confidence,source=excluded.source,updated_at=excluded.updated_at", series.ID, item.Season, item.Parent, marker.IntroStartTicks, marker.IntroEndTicks, marker.CreditsStartTicks, marker.IntroSamples, marker.CreditsSamples, max(marker.IntroSamples, marker.CreditsSamples), 1, marker.Source, updated.Unix())
	if err != nil {
		return err
	}
	a.intro.mu.Lock()
	a.storeIntroMarker(item.Parent, marker)
	a.intro.mu.Unlock()
	a.queueIntroRecord(IntroRecord{Version: 1, ItemID: item.ID, SeriesID: series.ID, Season: item.Season, Episode: item.Episode, IntroStartTicks: marker.IntroStartTicks, IntroEndTicks: marker.IntroEndTicks, CreditsStartTicks: marker.CreditsStartTicks, Source: marker.Source, Confidence: 1, UpdatedAt: updated}, candidate.generation)
	a.introLog(item.ID, fmt.Sprintf("%s · 第%d季 · %s：%02d:%02d · 学习完成", series.Name, item.Season, label, median/60, median%60), nil)
	return nil
}

func (a *App) introLog(itemID, message string, err error) {
	key := a.newActivity("intro_credits", itemID, message)
	a.finishActivity(key, err)
}

func (a *App) introChapters(item Item) []M {
	chapters := []M{}
	if item.Kind != "Episode" || !a.intro.enabled.Load() {
		return chapters
	}
	key := introMarkerKey(item.Parent, item.Season)
	a.intro.mu.Lock()
	marker, exists := a.intro.markers[key]
	overflow := a.intro.markerOverflow
	a.intro.mu.Unlock()
	if !exists && overflow {
		err := a.db.QueryRow("SELECT series_id,season,intro_start_ticks,intro_end_ticks,credits_start_ticks,intro_samples,credits_samples,source FROM intro_markers WHERE parent_id=? AND season=?", item.Parent, item.Season).Scan(&marker.SeriesID, &marker.Season, &marker.IntroStartTicks, &marker.IntroEndTicks, &marker.CreditsStartTicks, &marker.IntroSamples, &marker.CreditsSamples, &marker.Source)
		if err != nil {
			return chapters
		}
		a.intro.mu.Lock()
		a.storeIntroMarker(item.Parent, marker)
		a.intro.mu.Unlock()
	}
	if marker.IntroEndTicks > 0 {
		chapters = append(chapters, M{"Name": "片头开始", "StartPositionTicks": marker.IntroStartTicks, "MarkerType": "IntroStart"}, M{"Name": "片头结束", "StartPositionTicks": marker.IntroEndTicks, "MarkerType": "IntroEnd"})
	}
	if marker.CreditsStartTicks > 0 {
		chapters = append(chapters, M{"Name": "片尾开始", "StartPositionTicks": marker.CreditsStartTicks, "MarkerType": "CreditsStart"})
	}
	return chapters
}
