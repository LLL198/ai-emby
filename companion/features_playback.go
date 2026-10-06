package main

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/LLL198/ai-emby/security"
)

type featurePlaybackSample struct {
	Item     string
	Position float64
	At       time.Time
	Paused   bool
	Started  bool
}
type featurePlaybackConfig struct {
	Transcode      bool
	Threads        int
	Concurrency    int
	Bitrate        int
	CacheGB        int
	RetentionDays  int
	DanmakuURL     string
	DanmakuEnabled bool
}
type featurePlaybackPreference struct {
	AudioLanguage    string
	SubtitleLanguage string
	Subtitles        bool
	Danmaku          bool
}
type featureTranscode struct {
	ID, Item, Owner, Name, Directory string
	Started, Used                    time.Time
	Done                             bool
	Error                            string
	State                            string
	SourceMode                       string
	FallbackReason                   string
	Paused                           bool
	Downloaded, Total                int64
	cancel                           context.CancelFunc
	control                          *playbackTaskControl
	finished                         chan struct{}
	deleting                         bool
	deleted                          chan struct{}
	heartbeat                        time.Time
}

func (a *App) featurePlaybackConfig() featurePlaybackConfig {
	c := featurePlaybackConfig{Threads: 2, Concurrency: 2, Bitrate: 4000, CacheGB: 20, RetentionDays: 7}
	a.featureSetting("playback", &c)
	return c
}
func (a *App) featurePlaybackSettings(w http.ResponseWriter, r *http.Request, user User) {
	if !featureMethod(w, r, http.MethodGet, http.MethodPut) {
		return
	}
	c := featurePlaybackPreference{AudioLanguage: "zho", SubtitleLanguage: "zho", Subtitles: true}
	a.featureSetting("playback-user:"+user.ID, &c)
	if r.Method == http.MethodPut {
		if user.API {
			fail(w, 403, "需要用户登录")
			return
		}
		if !body(w, r, &c) {
			return
		}
		if len(c.AudioLanguage) > 16 || len(c.SubtitleLanguage) > 16 {
			fail(w, 400, "语言代码无效")
			return
		}
		if err := a.saveFeatureSetting("playback-user:"+user.ID, c); err != nil {
			featureError(w, err)
			return
		}
	}
	respond(w, M{"Preference": c, "Transcode": a.featurePlaybackConfig().Transcode, "Danmaku": a.featurePlaybackConfig().DanmakuEnabled})
}
func (a *App) featurePlaybackAdmin(w http.ResponseWriter, r *http.Request) {
	if !featureMethod(w, r, http.MethodGet, http.MethodPut) {
		return
	}
	c := a.featurePlaybackConfig()
	if r.Method == http.MethodPut {
		if !body(w, r, &c) {
			return
		}
		if c.Threads < 1 || c.Threads > 32 || c.Concurrency < 1 || c.Concurrency > 8 || c.Bitrate < 500 || c.Bitrate > 50000 || c.CacheGB < 1 || c.CacheGB > 2000 || c.RetentionDays < 1 || c.RetentionDays > 365 {
			fail(w, 400, "播放设置超出允许范围")
			return
		}
		if c.DanmakuURL != "" {
			u, err := url.Parse(c.DanmakuURL)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
				fail(w, 400, "弹幕地址需要为 HTTP/HTTPS 地址")
				return
			}
		}
		// Preserve the legacy protection choice before rewriting playback-only settings.
		protection, err := security.Load(r.Context(), a.db.DB)
		if err != nil {
			featureError(w, err)
			return
		}
		data, err := json.Marshal(protection)
		if err == nil {
			_, err = a.db.Exec("INSERT INTO settings(k,v) VALUES('feature:anti-theft',?) ON CONFLICT DO NOTHING", string(data))
		}
		if err != nil {
			featureError(w, err)
			return
		}
		if err := a.saveFeatureSetting("playback", c); err != nil {
			featureError(w, err)
			return
		}
	}
	history := []M{}
	rows, err := a.db.Query("SELECT h.user_id,u.name,h.item,COALESCE(i.name,''),h.day,h.seconds,h.count,h.updated FROM feature_play_history h JOIN users u ON u.id=h.user_id LEFT JOIN items i ON i.id=h.item ORDER BY h.updated DESC LIMIT 200")
	if err != nil {
		featureError(w, err)
		return
	}
	for rows.Next() {
		var uid, name, item, title, day string
		var seconds float64
		var count, updated int64
		if rows.Scan(&uid, &name, &item, &title, &day, &seconds, &count, &updated) == nil {
			history = append(history, M{"UserID": uid, "User": name, "Item": item, "Name": title, "Day": day, "Seconds": seconds, "Count": count, "Updated": updated})
		}
	}
	rows.Close()
	respond(w, M{"Settings": c, "History": history})
}

func (a *App) featurePlaybackEvent(w http.ResponseWriter, r *http.Request, user User) {
	if !featureMethod(w, r, http.MethodPost) {
		return
	}
	if user.API {
		respond(w, M{"ok": true})
		return
	}
	var request struct {
		ItemId        string
		PositionTicks int64
		IsPaused      bool
		Event         string
		PlaybackRate  float64
		Client        string
	}
	if !body(w, r, &request) {
		return
	}
	x, err := a.featureAccessibleItem(user, request.ItemId)
	if err != nil {
		featureError(w, err)
		return
	}
	if request.PositionTicks < 0 {
		fail(w, 400, "播放位置无效")
		return
	}
	if request.PlaybackRate <= 0 || request.PlaybackRate > 4 {
		request.PlaybackRate = 1
	}
	now := time.Now()
	key := user.ID + ":" + user.Device
	position := float64(request.PositionTicks) / 1e7
	a.features.playMu.Lock()
	previous, exists := a.features.plays[key]
	seconds, count := float64(0), 0
	if exists && previous.Item == x.ID {
		elapsed := now.Sub(previous.At).Seconds()
		delta := position - previous.Position
		if !previous.Paused && elapsed > 0 && elapsed <= 95 && delta >= 0 && delta <= elapsed*request.PlaybackRate+3 && request.Event != "seek" {
			seconds = math.Min(elapsed, delta/request.PlaybackRate)
		}
	} else if request.Event != "stopped" {
		count = 1
	}
	if request.Event == "stopped" {
		delete(a.features.plays, key)
	} else {
		a.features.plays[key] = featurePlaybackSample{Item: x.ID, Position: position, At: now, Paused: request.IsPaused}
	}
	a.features.playMu.Unlock()
	if seconds > 0 || count > 0 {
		_, err = a.db.Exec("INSERT INTO feature_play_history(user_id,item,day,seconds,count,updated) VALUES(?,?,?,?,?,?) ON CONFLICT(user_id,item,day) DO UPDATE SET seconds=feature_play_history.seconds+excluded.seconds,count=feature_play_history.count+excluded.count,updated=excluded.updated", user.ID, x.ID, now.UTC().Format("2006-01-02"), seconds, count, now.Unix())
		if err != nil {
			featureError(w, err)
			return
		}
	}
	client := request.Client
	if client == "" {
		client = r.UserAgent()
	}
	if len(client) > 256 {
		client = client[:256]
	}
	_, _ = a.db.Exec("INSERT INTO feature_devices(user_id,device,name,client,last_seen) VALUES(?,?,?,?,?) ON CONFLICT(user_id,device) DO UPDATE SET client=excluded.client,last_seen=excluded.last_seen", user.ID, user.Device, user.Device, client, now.Unix())
	respond(w, M{"ok": true})
}

func (a *App) featureDevicesAPI(w http.ResponseWriter, r *http.Request) {
	if !featureMethod(w, r, http.MethodGet, http.MethodPut, http.MethodDelete) {
		return
	}
	if r.Method != http.MethodGet {
		var request struct{ UserID, Device, Name string }
		if !body(w, r, &request) {
			return
		}
		if request.Device == "" || len(request.Name) > 128 {
			fail(w, 400, "设备信息无效")
			return
		}
		tx, err := a.db.Begin()
		if err != nil {
			featureError(w, err)
			return
		}
		defer tx.Rollback()
		if r.Method == http.MethodDelete {
			_, err = tx.Exec("DELETE FROM tokens WHERE user_id=? AND device=?", request.UserID, request.Device)
			if err == nil {
				_, err = tx.Exec("DELETE FROM plays WHERE user_id=? AND device=?", request.UserID, request.Device)
			}
			if err == nil {
				_, err = tx.Exec("DELETE FROM feature_devices WHERE user_id=? AND device=?", request.UserID, request.Device)
			}
		} else {
			_, err = tx.Exec("INSERT INTO feature_devices(user_id,device,name,last_seen) VALUES(?,?,?,?) ON CONFLICT(user_id,device) DO UPDATE SET name=excluded.name", request.UserID, request.Device, request.Name, featureNow())
		}
		if err != nil {
			featureError(w, err)
			return
		}
		if err = tx.Commit(); err != nil {
			featureError(w, err)
			return
		}
		respond(w, M{"ok": true})
		return
	}
	rows, err := a.db.Query(`SELECT t.user_id,u.name,t.device,COALESCE(d.name,t.device),COALESCE(d.client,''),COALESCE(d.last_seen,0),MAX(t.expires),COALESCE(MAX(p.updated),0) FROM tokens t JOIN users u ON u.id=t.user_id LEFT JOIN feature_devices d ON d.user_id=t.user_id AND d.device=t.device LEFT JOIN plays p ON p.user_id=t.user_id AND p.device=t.device WHERE t.expires>? GROUP BY t.user_id,u.name,t.device,d.name,d.client,d.last_seen ORDER BY COALESCE(d.last_seen,0) DESC`, featureNow())
	if err != nil {
		featureError(w, err)
		return
	}
	defer rows.Close()
	items := []M{}
	for rows.Next() {
		var uid, user, device, name, client string
		var last, expiry, playing int64
		if rows.Scan(&uid, &user, &device, &name, &client, &last, &expiry, &playing) == nil {
			items = append(items, M{"UserID": uid, "User": user, "Device": device, "Name": name, "Client": client, "LastSeen": last, "Persistent": expiry >= 253402300799, "Playing": featureNow()-playing < 90})
		}
	}
	respond(w, M{"Items": items})
}

func (a *App) featureMediaInput(x Item) (string, error) {
	input, err := a.featureRawMediaInput(x)
	if err != nil || strings.HasPrefix(input, "http") {
		return input, err
	}
	if info, err := os.Stat(input); err == nil && info.IsDir() {
		return "bluray:" + input, nil
	}
	if isoMediaCandidate(x) {
		info, err := inspectISOInput(context.Background(), input)
		if err != nil {
			return "", err
		}
		if info.Container == "iso" {
			return "bluray:" + input, nil
		}
	}
	return input, nil
}

func (a *App) featureRawMediaInput(x Item) (string, error) {
	raw := x.URL
	if strings.ToLower(filepath.Ext(x.Path)) != ".strm" {
		raw = x.Path
	}
	u, err := url.Parse(raw)
	if err != nil || raw == "" {
		return "", errors.New("无有效媒体源")
	}
	if u.Scheme == "http" || u.Scheme == "https" {
		return a.cloudInternalInput(raw)
	}
	if u.Scheme != "" && u.Scheme != "file" {
		return "", errors.New("媒体协议不支持")
	}
	local := raw
	if u.Scheme == "file" {
		local = u.Path
	}
	real, err := filepath.EvalSymlinks(local)
	if err != nil || !allowedMediaPath(real) {
		return "", errors.New("本地媒体路径不可访问")
	}
	if _, err := os.Stat(real); err != nil {
		return "", err
	}
	return real, nil
}
func featureInputArgs(input string) []string {
	protocols := "file,bluray,concat"
	if strings.HasPrefix(input, "http") {
		protocols = "http,https,tcp,tls,crypto"
	}
	args := []string{"-protocol_whitelist", protocols, "-probesize", "5000000", "-analyzeduration", "5000000"}
	if strings.HasPrefix(input, "http") {
		args = append(args, "-rw_timeout", "20000000")
	}
	return args
}

func (a *App) featureCapture(ctx context.Context, x Item, seconds float64) error {
	if seconds < 0 || seconds > 86400 {
		return errors.New("截帧位置无效")
	}
	if seconds == 0 {
		seconds = 30
	}
	input, err := a.featureMediaInput(x)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	args := append([]string{"-v", "error"}, featureInputArgs(input)...)
	args = append(args, "-threads", "2", "-ss", strconv.FormatFloat(seconds, 'f', 2, 64), "-i", input, "-frames:v", "1", "-vf", "scale=960:540:force_original_aspect_ratio=decrease", "-f", "image2pipe", "-c:v", "mjpeg", "-threads", "2", "pipe:1")
	data, err := exec.CommandContext(ctx, "ffmpeg", args...).Output()
	if err != nil {
		return errors.New("截帧失败，请检查媒体是否可播放")
	}
	return a.featureSaveArtwork(ctx, x, "Primary", data, a.featurePolicy(x.Lib).WriteArtwork)
}

func (a *App) featureExtractChapters(ctx context.Context, x Item) error {
	input, err := a.featureMediaInput(x)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	args := append([]string{"-v", "error"}, featureInputArgs(input)...)
	args = append(args, "-i", input, "-show_chapters", "-of", "json")
	data, err := exec.CommandContext(ctx, "ffprobe", args...).Output()
	if err != nil {
		return errors.New("读取章节失败")
	}
	var result struct {
		Chapters []struct {
			Start string `json:"start_time"`
			End   string `json:"end_time"`
			Tags  map[string]string
		}
	}
	if err = json.Unmarshal(data, &result); err != nil {
		return err
	}
	chapters := []M{}
	for _, chapter := range result.Chapters {
		title := strings.ToLower(chapter.Tags["title"])
		start, _ := strconv.ParseFloat(chapter.Start, 64)
		end, _ := strconv.ParseFloat(chapter.End, 64)
		kind := "chapter"
		if strings.Contains(title, "intro") || strings.Contains(title, "opening") || strings.Contains(title, "片头") {
			kind = "intro"
		}
		if strings.Contains(title, "credit") || strings.Contains(title, "ending") || strings.Contains(title, "片尾") {
			kind = "credits"
		}
		chapters = append(chapters, M{"Name": chapter.Tags["title"], "Start": start, "End": end, "Kind": kind})
	}
	_, err = a.db.Exec("INSERT INTO feature_chapters(item,data,updated) VALUES(?,?,?) ON CONFLICT(item) DO UPDATE SET data=excluded.data,updated=excluded.updated", x.ID, featureJSON(chapters), featureNow())
	return err
}

func (a *App) featurePlaybackAPI(w http.ResponseWriter, r *http.Request, user User) {
	if !featureMethod(w, r, http.MethodPost) {
		return
	}
	if user.API {
		fail(w, 403, "需要用户登录")
		return
	}
	var request struct {
		ID      string
		Audio   int
		Start   float64
		Session string
	}
	request.Audio = -1
	if !body(w, r, &request) {
		return
	}
	x, err := a.featureAccessibleItem(user, request.ID)
	if err != nil {
		featureError(w, err)
		return
	}
	c := a.featurePlaybackConfig()
	if !a.canPlay(user.ID) {
		fail(w, 403, "该账号没有播放权限")
		return
	}
	if err = a.reserve(user, x.ID); err != nil {
		fail(w, 403, "播放设备数量已达上限")
		return
	}
	if !c.Transcode {
		fail(w, 409, "管理员尚未启用转码")
		return
	}
	if request.Start < 0 || request.Start > 86400 || request.Audio < -1 || request.Audio > 200 || (request.Session != "" && (len(request.Session) != 32 || strings.Trim(request.Session, "0123456789abcdef") != "")) {
		fail(w, 400, "播放参数无效")
		return
	}
	input, err := a.featureRawMediaInput(x)
	if err != nil {
		featureError(w, err)
		return
	}
	var isoInfo isoMediaInfo
	if isoMediaCandidate(x) {
		isoInfo, err = inspectISOInput(r.Context(), input)
		if err != nil {
			fail(w, 502, err.Error())
			return
		}
		if isoInfo.Container == "unknown" {
			fail(w, 415, "无法识别 ISO 的实际格式，文件可能损坏或并非视频")
			return
		}
	} else if info, err := os.Stat(input); err == nil && info.IsDir() {
		input = "bluray:" + input
	}
	key := digest(x.ID + digest(x.URL) + strconv.FormatInt(x.Mtime, 10) + user.ID + featureJSON(request) + featureJSON(c))[:40]
	dir := filepath.Join(featureDataRoot(), "playback", key)
	var job *featureTranscode
	for {
		a.features.cacheMu.Lock()
		job = a.features.transcodes[key]
		if job == nil || !job.deleting {
			break
		}
		deleted := job.deleted
		a.features.cacheMu.Unlock()
		// A quick reopen waits for the previous tools and cache removal to finish.
		timer := time.NewTimer(10 * time.Second)
		select {
		case <-deleted:
			timer.Stop()
		case <-r.Context().Done():
			timer.Stop()
			return
		case <-timer.C:
			fail(w, 409, "上次播放仍在清理，请稍后重试")
			return
		}
	}
	if job != nil && job.Done && job.Error != "" {
		if job.finished != nil {
			select {
			case <-job.finished:
			default:
				a.features.cacheMu.Unlock()
				fail(w, 409, "旧播放任务正在退出，请稍后重试")
				return
			}
		}
		delete(a.features.transcodes, key)
		job = nil
	}
	if job == nil {
		active := 0
		for _, j := range a.features.transcodes {
			if !j.Done {
				active++
			}
		}
		if active >= c.Concurrency {
			a.features.cacheMu.Unlock()
			fail(w, 409, "转码任务已满，请稍后重试")
			return
		}
		if err = os.MkdirAll(dir, 0700); err != nil {
			a.features.cacheMu.Unlock()
			featureError(w, err)
			return
		}
		ctx, cancel := context.WithCancel(a.features.ctx)
		job = &featureTranscode{ID: key, Item: x.ID, Owner: user.ID, Name: x.Name, Directory: dir, Started: time.Now(), Used: time.Now(), State: "queued", cancel: cancel, control: newPlaybackTaskControl(), finished: make(chan struct{})}
		a.features.transcodes[key] = job
		if data, e := os.ReadFile(filepath.Join(dir, "record.json")); e == nil {
			var record featureTranscode
			output, outputErr := os.Stat(filepath.Join(dir, "video.mp4"))
			if json.Unmarshal(data, &record) == nil && record.Done && record.Error == "" && outputErr == nil && output.Mode().IsRegular() && output.Size() >= 32 {
				job.Done, job.State = true, "complete"
				job.SourceMode, job.FallbackReason = record.SourceMode, record.FallbackReason
			}
		}
		if !job.Done {
			_ = os.WriteFile(filepath.Join(dir, "record.json"), []byte(featureJSON(job)), 0600)
			_ = os.Remove(filepath.Join(dir, "video.mp4"))
			a.features.wg.Add(1)
			go func() {
				defer a.features.wg.Done()
				defer close(job.finished)
				defer cancel()
				var err error
				var online *isoOnlineInput
				if isoInfo.Container == "iso" {
					online, err = a.prepareISOOnline(ctx, input, isoInfo, job)
					if err == nil {
						input = online.URL
						defer online.close()
						a.features.cacheMu.Lock()
						job.SourceMode = online.Mode
						a.features.cacheMu.Unlock()
					} else if ctx.Err() == nil {
						a.features.cacheMu.Lock()
						job.SourceMode = "cache"
						job.FallbackReason = "镜像结构暂不支持在线解析"
						if errors.Is(err, errISORangeUnavailable) {
							job.FallbackReason = "网盘源不支持可靠的分段读取"
						}
						if errors.Is(err, context.DeadlineExceeded) {
							job.FallbackReason = "在线读取镜像目录超时"
						}
						if errors.Is(err, errISODiscReaderUnavailable) {
							job.FallbackReason = "蓝光按需读盘组件未安装"
						}
						a.features.cacheMu.Unlock()
						input, err = a.prepareISOPlayback(ctx, input, dir, isoInfo, job)
					}
				}
				if err == nil {
					copyVideo := false
					if online != nil {
						probeCtx, probeCancel := playbackTaskTimeout(ctx, job.control, 20*time.Second)
						probeArgs := append([]string{"-v", "error"}, featureInputArgs(input)...)
						probeArgs = append(probeArgs, online.Args...)
						probeArgs = append(probeArgs, "-i", input, "-show_streams", "-of", "json")
						var probe struct {
							Streams []struct {
								CodecType   string `json:"codec_type"`
								CodecName   string `json:"codec_name"`
								PixelFormat string `json:"pix_fmt"`
							}
						}
						var output transferLogBuffer
						probeCommand := exec.CommandContext(probeCtx, "ffprobe", probeArgs...)
						probeCommand.Stdout = &output
						probeErr := job.control.run(probeCtx, probeCommand)
						if probeErr == nil {
							probeErr = json.Unmarshal(output.Bytes(), &probe)
						}
						probeCancel()
						if probeErr == nil {
							for _, stream := range probe.Streams {
								if stream.CodecType == "video" {
									copyVideo = request.Start == 0 && stream.CodecName == "h264" && (stream.PixelFormat == "yuv420p" || stream.PixelFormat == "yuvj420p")
									break
								}
							}
						}
					}
					a.features.cacheMu.Lock()
					job.State = "transcoding"
					if copyVideo {
						job.State = "remuxing"
					}
					a.features.cacheMu.Unlock()
					args := append([]string{"-v", "error", "-nostdin", "-y"}, featureInputArgs(input)...)
					if online != nil {
						args = append(args, online.Args...)
					}
					args = append(args, "-ss", strconv.FormatFloat(request.Start, 'f', 3, 64), "-i", input, "-map", "0:v:0?")
					audio := "0:a:0?"
					if request.Audio >= 0 {
						audio = "0:" + strconv.Itoa(request.Audio)
					}
					args = append(args, "-map", audio)
					if copyVideo {
						args = append(args, "-c:v", "copy")
					} else {
						args = append(args, "-c:v", "libx264", "-preset", "veryfast", "-pix_fmt", "yuv420p", "-threads", strconv.Itoa(c.Threads), "-b:v", strconv.Itoa(c.Bitrate)+"k")
					}
					args = append(args, "-c:a", "aac", "-ac", "2", "-b:a", "192k", "-movflags", "+frag_keyframe+empty_moov+default_base_moof", "-f", "mp4", filepath.Join(dir, "video.mp4"))
					if job.control.run(ctx, exec.CommandContext(ctx, "ffmpeg", args...)) != nil {
						err = errors.New("转码失败，请检查媒体源、音轨或 FFmpeg 支持")
					}
				}
				a.features.cacheMu.Lock()
				job.Done, job.State, job.Paused = true, "complete", false
				if err != nil {
					job.Error, job.State = err.Error(), "failed"
				}
				if ctx.Err() != nil {
					job.State, job.Error = "stopped", "播放任务已停止"
				}
				if job.deleting {
					job.State = "deleting"
				}
				if !job.deleting {
					_ = os.WriteFile(filepath.Join(dir, "record.json"), []byte(featureJSON(job)), 0600)
				}
				a.features.cacheMu.Unlock()
			}()
		} else {
			cancel()
			close(job.finished)
		}
	}
	job.Used = time.Now()
	job.heartbeat = job.Used
	snapshot := *job
	a.features.cacheMu.Unlock()
	respond(w, M{"ID": key, "URL": "/features/stream/" + key + "/video.mp4", "Done": snapshot.Done, "Error": snapshot.Error, "Start": request.Start})
}

func (a *App) featureStream(w http.ResponseWriter, r *http.Request, user User) {
	if !featureMethod(w, r, http.MethodGet, http.MethodHead) {
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/features/stream/"), "/")
	if len(parts) != 2 || len(parts[0]) != 40 || strings.Trim(parts[0], "0123456789abcdef") != "" || parts[1] != "video.mp4" {
		http.NotFound(w, r)
		return
	}
	a.features.cacheMu.Lock()
	job := a.features.transcodes[parts[0]]
	var record featureTranscode
	if job != nil {
		job.Used = time.Now()
		record = *job
	}
	a.features.cacheMu.Unlock()
	if job == nil {
		data, err := os.ReadFile(filepath.Join(featureDataRoot(), "playback", parts[0], "record.json"))
		if err != nil || json.Unmarshal(data, &record) != nil {
			http.NotFound(w, r)
			return
		}
		record.Directory = filepath.Join(featureDataRoot(), "playback", parts[0])
		record.Used = time.Now()
		if record.Done {
			_ = os.WriteFile(filepath.Join(record.Directory, "record.json"), []byte(featureJSON(record)), 0600)
			a.features.cacheMu.Lock()
			if a.features.transcodes[parts[0]] == nil {
				copy := record
				a.features.transcodes[parts[0]] = &copy
			}
			a.features.cacheMu.Unlock()
		}
	}
	if record.Owner != user.ID && !user.Admin {
		fail(w, 404, "播放缓存不存在")
		return
	}
	if _, err := a.featureAccessibleItem(user, record.Item); err != nil {
		featureError(w, err)
		return
	}
	if !a.canPlay(user.ID) {
		fail(w, 403, "该账号没有播放权限")
		return
	}
	if record.Error != "" {
		fail(w, 502, record.Error)
		return
	}
	if record.Done {
		http.ServeFile(w, r, filepath.Join(record.Directory, "video.mp4"))
		return
	}
	// An active fragmented MP4 is streamed until ffmpeg finishes; its length is not fixed yet.
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Accept-Ranges", "none")
	if r.Method == http.MethodHead {
		return
	}
	var file *os.File
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	var sent bool
	for {
		select {
		case <-r.Context().Done():
			if file != nil {
				file.Close()
			}
			return
		case <-ticker.C:
			if file == nil {
				file, _ = os.Open(filepath.Join(record.Directory, "video.mp4"))
			}
			if file != nil {
				if n, _ := io.Copy(w, file); n > 0 {
					sent = true
					if flusher, ok := w.(http.Flusher); ok {
						flusher.Flush()
					}
				}
			}
			a.features.cacheMu.Lock()
			done, errorText := job.Done, job.Error
			job.Used = time.Now()
			a.features.cacheMu.Unlock()
			if done {
				if file != nil {
					io.Copy(w, file)
					file.Close()
				}
				if !sent && errorText != "" {
					fail(w, 502, errorText)
				}
				return
			}
		}
	}
}

func (a *App) featureSubtitleAPI(w http.ResponseWriter, r *http.Request, user User) {
	if !featureMethod(w, r, http.MethodGet) {
		return
	}
	x, err := a.featureAccessibleItem(user, r.URL.Query().Get("ID"))
	if err != nil {
		featureError(w, err)
		return
	}
	if !a.canPlay(user.ID) {
		fail(w, 403, "该账号没有播放权限")
		return
	}
	index, err := strconv.Atoi(r.URL.Query().Get("Index"))
	if err != nil || index < 0 || index > 200 {
		fail(w, 400, "字幕轨道无效")
		return
	}
	input, err := a.featureMediaInput(x)
	if err != nil {
		featureError(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	args := append([]string{"-v", "error", "-nostdin"}, featureInputArgs(input)...)
	args = append(args, "-i", input, "-map", "0:"+strconv.Itoa(index), "-f", "webvtt", "pipe:1")
	command := exec.CommandContext(ctx, "ffmpeg", args...)
	pipe, err := command.StdoutPipe()
	if err != nil {
		fail(w, 502, "字幕提取失败")
		return
	}
	if err = command.Start(); err != nil {
		fail(w, 502, "字幕提取失败")
		return
	}
	data, err := io.ReadAll(io.LimitReader(pipe, 8<<20))
	if len(data) >= 8<<20 || err != nil {
		cancel()
		_ = command.Wait()
		fail(w, 502, "字幕过大或读取失败")
		return
	}
	if err = command.Wait(); err != nil {
		fail(w, 415, "无法转换此字幕，图像字幕需使用兼容播放器")
		return
	}
	w.Header().Set("Content-Type", "text/vtt; charset=utf-8")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	_, _ = w.Write(data)
}

type featureComment struct {
	Time  float64
	Text  string
	Color string
	Mode  int
}

func featureParseDanmaku(data []byte) ([]featureComment, error) {
	var raw struct {
		Comments []struct {
			P    string `xml:"p,attr"`
			Text string `xml:",chardata"`
		} `xml:"d"`
	}
	if err := xml.Unmarshal(data, &raw); err != nil {
		return nil, errors.New("弹幕 XML 格式无效")
	}
	comments := []featureComment{}
	for _, v := range raw.Comments {
		p := strings.Split(v.P, ",")
		if len(p) < 4 {
			continue
		}
		seconds, _ := strconv.ParseFloat(p[0], 64)
		mode, _ := strconv.Atoi(p[1])
		color, _ := strconv.Atoi(p[3])
		if seconds < 0 || seconds > 86400 || len(v.Text) > 500 {
			continue
		}
		comments = append(comments, featureComment{Time: seconds, Text: v.Text, Mode: mode, Color: fmt.Sprintf("#%06x", color&0xffffff)})
		if len(comments) >= 50000 {
			break
		}
	}
	return comments, nil
}

var featureDanmakuLock sync.Mutex

func (a *App) featureDanmakuAPI(w http.ResponseWriter, r *http.Request, user User) {
	if !featureMethod(w, r, http.MethodGet, http.MethodPost, http.MethodDelete) {
		return
	}
	x, err := a.featureAccessibleItem(user, r.URL.Query().Get("ID"))
	if err != nil {
		featureError(w, err)
		return
	}
	path := filepath.Join(featureDataRoot(), "danmaku", digest(x.Path)+".xml")
	if r.Method != http.MethodGet {
		if !user.Admin || user.API {
			fail(w, 403, "需要管理员账号")
			return
		}
		featureDanmakuLock.Lock()
		defer featureDanmakuLock.Unlock()
		if r.Method == http.MethodDelete {
			err = os.Remove(path)
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				featureError(w, err)
				return
			}
			respond(w, M{"ok": true})
			return
		}
		data, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 8<<20))
		if e != nil {
			fail(w, 400, "弹幕文件超过 8MB")
			return
		}
		if _, e = featureParseDanmaku(data); e != nil {
			fail(w, 400, e.Error())
			return
		}
		if e = os.MkdirAll(filepath.Dir(path), 0700); e == nil {
			e = os.WriteFile(path, data, 0600)
		}
		if e != nil {
			featureError(w, e)
			return
		}
		respond(w, M{"ok": true})
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		c := a.featurePlaybackConfig()
		if !c.DanmakuEnabled || c.DanmakuURL == "" {
			respond(w, M{"Items": []featureComment{}})
			return
		}
		key, _ := a.tmdbIdentity(x)
		endpoint := strings.NewReplacer("{item}", url.QueryEscape(x.Name), "{tmdb}", url.QueryEscape(key), "{season}", strconv.Itoa(x.Season), "{episode}", strconv.Itoa(x.Episode)).Replace(c.DanmakuURL)
		req, e := http.NewRequestWithContext(r.Context(), "GET", endpoint, nil)
		if e != nil {
			fail(w, 400, "弹幕地址无效")
			return
		}
		response, e := (&http.Client{Timeout: 20 * time.Second}).Do(req)
		if e != nil {
			fail(w, 502, "弹幕服务不可用")
			return
		}
		defer response.Body.Close()
		if response.StatusCode != 200 {
			fail(w, 502, "弹幕服务返回失败")
			return
		}
		data, e = io.ReadAll(io.LimitReader(response.Body, 8<<20))
		if e != nil {
			fail(w, 502, "读取弹幕失败")
			return
		}
	}
	comments, err := featureParseDanmaku(data)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	respond(w, M{"Items": comments})
}
