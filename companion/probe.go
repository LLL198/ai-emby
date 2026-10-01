package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

func (a *App) probeMedia(w http.ResponseWriter, r *http.Request, u User, i string) {
	if !u.Admin || u.API {
		fail(w, 403, "需要管理员账号")
		return
	}
	if r.Method != "POST" {
		fail(w, 405, "POST required")
		return
	}
	x, e := a.item(i)
	if e != nil || x.URL == "" {
		fail(w, 404, "无可播放源")
		return
	}
	job := a.queueProbe(x, token(r), r.UserAgent(), false)
	if job == nil {
		fail(w, 503, "提取队列已满，请稍后重试")
		return
	}
	select {
	case <-job.done:
		if job.err != nil {
			fail(w, 502, job.err.Error())
			return
		}
		respond(w, job.data)
	case <-r.Context().Done():
	}
}

func (a *App) extractMedia(ctx context.Context, x Item, t, ua string) (M, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	base := strings.TrimRight(os.Getenv("PROBE_PLAYBACK_URL"), "/")
	if base == "" {
		base = strings.TrimRight(os.Getenv("PUBLIC_PLAYBACK_URL"), "/")
	}
	if base == "" {
		base = "http://127.0.0.1:8097"
	}
	if ua == "" {
		ua = "GoEmby-MediaInfo/1.0"
	}
	cmd := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-rw_timeout", "20000000", "-user_agent", ua, "-probesize", "5000000", "-analyzeduration", "5000000", "-protocol_whitelist", "http,https,tcp,tls,crypto", "-show_entries", "format=duration,format_name,size,bit_rate:stream=index,codec_name,codec_type,width,height,channels,channel_layout,sample_rate,bit_rate,avg_frame_rate,r_frame_rate,profile,level,display_aspect_ratio,field_order,bits_per_raw_sample,pix_fmt,color_transfer,color_primaries:stream_tags=language,title:stream_disposition=default,forced", "-of", "json", base+a.playURL(x, t)+"&GoEmbyProbe=true")
	data, e := cmd.Output()
	if e != nil {
		return nil, probeFailure(ctx, e)
	}
	var b struct {
		Format struct {
			Duration string `json:"duration"`
			Name     string `json:"format_name"`
			Size     string `json:"size"`
			Bitrate  string `json:"bit_rate"`
		} `json:"format"`
		Streams []struct {
			FrameRate     string `json:"avg_frame_rate"`
			RealFrameRate string `json:"r_frame_rate"`
			Profile       string `json:"profile"`
			Level         int    `json:"level"`
			Aspect        string `json:"display_aspect_ratio"`
			FieldOrder    string `json:"field_order"`
			BitDepth      string `json:"bits_per_raw_sample"`
			PixelFormat   string `json:"pix_fmt"`
			Transfer      string `json:"color_transfer"`
			Primaries     string `json:"color_primaries"`
			Layout        string `json:"channel_layout"`
			Disposition   struct {
				Default int `json:"default"`
				Forced  int `json:"forced"`
			} `json:"disposition"`
			Index    int               `json:"index"`
			Codec    string            `json:"codec_name"`
			Type     string            `json:"codec_type"`
			Width    int               `json:"width"`
			Height   int               `json:"height"`
			Channels int               `json:"channels"`
			Sample   string            `json:"sample_rate"`
			Bitrate  string            `json:"bit_rate"`
			Tags     map[string]string `json:"tags"`
		} `json:"streams"`
	}
	if json.Unmarshal(data, &b) != nil {
		return nil, fmt.Errorf("无效媒体信息")
	}
	streams := []M{}
	for _, v := range b.Streams {
		kind := ""
		if v.Type == "video" {
			kind = "Video"
		}
		if v.Type == "subtitle" {
			kind = "Subtitle"
		}
		if v.Type == "audio" {
			kind = "Audio"
		}
		if kind == "" {
			continue
		}
		m := M{"Index": v.Index, "Type": kind, "Codec": v.Codec, "IsExternal": false, "Language": v.Tags["language"], "DisplayTitle": v.Tags["title"]}
		m["IsDefault"] = v.Disposition.Default == 1
		m["IsForced"] = v.Disposition.Forced == 1
		if kind == "Video" {
			m["Profile"] = v.Profile
			if v.Level > 0 {
				m["Level"] = float64(v.Level)
			}
			if v.Aspect != "" {
				m["AspectRatio"] = v.Aspect
			}
			if n := parseFrameRate(v.FrameRate); n > 0 {
				m["AverageFrameRate"] = n
			}
			if n := parseFrameRate(v.RealFrameRate); n > 0 {
				m["RealFrameRate"] = n
			}
			if v.FieldOrder != "" && v.FieldOrder != "unknown" {
				m["IsInterlaced"] = v.FieldOrder != "progressive"
			}
			m["PixelFormat"] = v.PixelFormat
			if n, _ := strconv.Atoi(v.BitDepth); n > 0 {
				m["BitDepth"] = n
			} else if n := pixelBitDepth(v.PixelFormat); n > 0 {
				m["BitDepth"] = n
			}
			if v.Transfer != "" && v.Transfer != "unknown" {
				m["VideoRange"] = "SDR"
				if v.Transfer == "smpte2084" || v.Transfer == "arib-std-b67" {
					m["VideoRange"] = "HDR"
				}
				m["ColorTransfer"] = v.Transfer
			}
			if v.Primaries != "" {
				m["ColorPrimaries"] = v.Primaries
			}
			if v.Tags["title"] == "" {
				m["DisplayTitle"] = fmt.Sprintf("%dp %s", v.Height, strings.ToUpper(v.Codec))
			}
		}
		if kind == "Audio" {
			m["ChannelLayout"] = v.Layout
			if v.Tags["title"] == "" {
				title := strings.TrimSpace(strings.ToUpper(v.Codec) + " " + v.Layout)
				if v.Disposition.Default == 1 {
					title += "（默认）"
				}
				m["DisplayTitle"] = title
			}
		}
		if v.Width > 0 {
			m["Width"] = v.Width
			m["Height"] = v.Height
		}
		if v.Channels > 0 {
			m["Channels"] = v.Channels
		}
		if n, _ := strconv.Atoi(v.Sample); n > 0 {
			m["SampleRate"] = n
		}
		if n, _ := strconv.Atoi(v.Bitrate); n > 0 {
			m["BitRate"] = n
		}
		streams = append(streams, m)
	}
	if len(streams) == 0 {
		return nil, fmt.Errorf("未识别到音视频流")
	}
	m := M{"MediaStreams": streams}
	duration, _ := strconv.ParseFloat(b.Format.Duration, 64)
	size, _ := strconv.ParseInt(b.Format.Size, 10, 64)
	if size <= 0 {
		size = remoteMediaSize(ctx, base+a.playURL(x, t)+"&GoEmbyProbe=true", ua)
	}
	if size > 0 {
		m["Size"] = size
	}
	if n, _ := strconv.ParseInt(b.Format.Bitrate, 10, 64); n <= 0 && size > 0 && duration > 0 {
		m["Bitrate"] = int64(float64(size) * 8 / duration)
	}
	if n, _ := strconv.ParseFloat(b.Format.Duration, 64); n > 0 {
		m["RunTimeTicks"] = int64(n * 1e7)
	}
	if n, _ := strconv.ParseInt(b.Format.Size, 10, 64); n > 0 {
		m["Size"] = n
	}
	if n, _ := strconv.Atoi(b.Format.Bitrate); n > 0 {
		m["Bitrate"] = n
	}
	name := b.Format.Name
	switch {
	case strings.Contains(name, "matroska"):
		name = "mkv"
	case strings.Contains(name, "mp4"):
		name = "mp4"
	case name == "mpegts":
		name = "ts"
	}
	m["Container"] = name

	if e = a.saveMedia(x, m); e != nil {
		return nil, fmt.Errorf("保存媒体信息失败：%w", e)
	}
	return m, nil
}
func (a *App) cachedMedia(x Item) M {
	var s string
	m := M{}
	if a.db.QueryRow("SELECT data FROM media_probe WHERE item=? AND source=?", x.ID, digest(x.URL)).Scan(&s) == nil {
		json.Unmarshal([]byte(s), &m)
		return m
	}
	if a.db.QueryRow("SELECT data FROM media_probe WHERE source=? ORDER BY item LIMIT 1", digest(x.URL)).Scan(&s) == nil {
		if json.Unmarshal([]byte(s), &m) == nil && m["Partial"] != true {
			return m
		}
	}
	cfg := a.probeSettings()
	if cfg.Persistent {
		return a.archivedMedia(x, cfg.Directory)
	}
	return m
}

// Remote ffprobe inputs may omit format.size; a one-byte range exposes total size.
func remoteMediaSize(ctx context.Context, address, ua string) int64 {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, e := http.NewRequestWithContext(ctx, "GET", address, nil)
	if e != nil {
		return 0
	}
	req.Header.Set("Range", "bytes=0-0")
	req.Header.Set("User-Agent", ua)
	client := &http.Client{Timeout: 5 * time.Second}
	res, e := client.Do(req)
	if e != nil {
		return 0
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusPartialContent {
		parts := strings.Split(res.Header.Get("Content-Range"), "/")
		if len(parts) == 2 {
			n, _ := strconv.ParseInt(parts[1], 10, 64)
			if n > 0 {
				return n
			}
		}
	}
	if res.StatusCode == http.StatusOK && res.ContentLength > 0 {
		return res.ContentLength
	}
	return 0
}

func parseFrameRate(s string) float64 {
	parts := strings.Split(s, "/")
	n, _ := strconv.ParseFloat(parts[0], 64)
	if len(parts) == 2 {
		d, _ := strconv.ParseFloat(parts[1], 64)
		if d <= 0 {
			return 0
		}
		n /= d
	}
	return n
}
func pixelBitDepth(s string) int {
	for _, n := range []int{16, 14, 12, 10, 9} {
		if strings.Contains(s, "p"+strconv.Itoa(n)) || strings.Contains(s, "p0"+strconv.Itoa(n)) {
			return n
		}
	}
	switch s {
	case "yuv420p", "yuv422p", "yuv444p", "nv12", "nv21", "rgb24", "bgr24", "yuvj420p", "yuvj422p", "yuvj444p":
		return 8
	}
	return 0
}
