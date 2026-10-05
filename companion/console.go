package main

import (
	"bufio"
	"fmt"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"
)

var processStarted = time.Now()

func (sample *dashboardCPUSample) percent() float64 {
	seconds, err := dashboardCPUSeconds()
	if err != nil {
		return 0
	}
	now := time.Now()
	sample.mu.Lock()
	defer sample.mu.Unlock()
	previousAt, previousSeconds := sample.at, sample.seconds
	if previousAt.IsZero() {
		previousAt = processStarted
	}
	sample.at, sample.seconds = now, seconds
	elapsed, used := now.Sub(previousAt).Seconds(), seconds-previousSeconds
	if elapsed <= 0 || used < 0 {
		return 0
	}
	return used / elapsed * 100
}

func dashboardMemoryBytes() uint64 {
	if file, err := os.Open("/proc/self/status"); err == nil {
		defer file.Close()
		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "VmRSS:") {
				continue
			}
			fields := strings.Fields(line)
			if len(fields) > 1 {
				if kb, err := strconv.ParseUint(fields[1], 10, 64); err == nil {
					return kb * 1024
				}
			}
		}
	}
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	return memory.Alloc
}

func padEpisodeNumber(number int) string {
	if number < 10 {
		return "0" + strconv.Itoa(number)
	}
	return strconv.Itoa(number)
}

func (a *App) adminDashboard(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		fail(w, 405, "GET required")
		return
	}
	counts := make([]int, 3)
	for index, kind := range []string{"Movie", "Series", "Episode"} {
		if err := a.db.QueryRow("SELECT count(*) FROM items WHERE kind=?", kind).Scan(&counts[index]); err != nil {
			fail(w, 500, "读取媒体统计失败")
			return
		}
	}
	var users int
	if err := a.db.QueryRow("SELECT count(*) FROM users").Scan(&users); err != nil {
		fail(w, 500, "读取用户统计失败")
		return
	}
	type active struct {
		userID, deviceKey, itemID, username, name, kind, parent, series string
		season, episode                                                 int
	}
	rows, err := a.db.Query(`SELECT p.user_id,p.device,p.item,u.name,i.name,i.kind,i.season,i.episode,
	 COALESCE(parent.name,''),COALESCE(series.name,'')
	 FROM plays p JOIN users u ON u.id=p.user_id JOIN items i ON i.id=p.item
	 LEFT JOIN items parent ON parent.id=i.parent LEFT JOIN items series ON series.id=parent.parent
	 WHERE p.updated>=? ORDER BY p.updated DESC`, time.Now().Add(-3*time.Minute).Unix())
	if err != nil {
		fail(w, 500, "读取播放状态失败")
		return
	}
	playing := []active{}
	for rows.Next() {
		var entry active
		err = rows.Scan(&entry.userID, &entry.deviceKey, &entry.itemID, &entry.username, &entry.name, &entry.kind, &entry.season, &entry.episode, &entry.parent, &entry.series)
		if err != nil {
			break
		}
		playing = append(playing, entry)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		fail(w, 500, "读取播放状态失败")
		return
	}
	output := make([]dashboardPlay, 0, len(playing))
	a.activity.mu.Lock()
	for _, entry := range playing {
		mediaName := entry.name
		if entry.kind == "Episode" {
			series := entry.series
			if series == "" {
				series = entry.parent
			}
			if series != "" {
				mediaName = fmt.Sprintf("%s · S%sE%s · 第%d集", series, padEpisodeNumber(entry.season), padEpisodeNumber(entry.episode), entry.episode)
			}
		}
		play := dashboardPlay{Username: entry.username, MediaName: mediaName, Device: "未知设备"}
		for index := len(a.activity.entries) - 1; index >= 0; index-- {
			log := a.activity.entries[index]
			if log.Category != "playback" || log.State != "requested" || log.UserID != entry.userID || log.DeviceKey != entry.deviceKey || log.ItemID != entry.itemID {
				continue
			}
			if log.Device != "" {
				play.Device = strings.TrimSuffix(log.Device, " ("+entry.deviceKey+")")
			}
			play.Client, play.ProgressPercent = log.Client, log.Progress
			break
		}
		output = append(output, play)
	}
	a.activity.mu.Unlock()
	w.Header().Set("Cache-Control", "no-store")
	respond(w, M{"uptimeSeconds": int64(time.Since(processStarted).Seconds()), "cpuPercent": a.dashboardCPU.percent(), "memoryBytes": dashboardMemoryBytes(), "movieCount": counts[0], "seriesCount": counts[1], "episodeCount": counts[2], "userCount": users, "activePlaybackCount": len(output), "activePlayback": output})
}
