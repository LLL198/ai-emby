package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const hlsSegmentSeconds = 4.0
const hlsSegmentLimit = 16 << 20
const hlsCacheLimit = 32 << 20

type hlsPoint struct {
	time   float64
	offset int64
}
type hlsFragment struct {
	init, media []byte
	used        time.Time
}
type hlsFlight struct {
	index    int
	done     chan struct{}
	ctx      context.Context
	cancel   context.CancelFunc
	waiters  int
	err      error
	fragment *hlsFragment
}
type hlsPlayback struct {
	ctx                context.Context
	ready              atomic.Bool
	mu                 sync.Mutex
	points             []hlsPoint
	indexed, copyVideo bool
	codec              string
	audio              bool
	requests           chan *hlsFlight
	flights            map[int]*hlsFlight
	cache              map[int]*hlsFragment
	bytes              int
}

func newHLSPlayback(ctx context.Context) *hlsPlayback {
	return &hlsPlayback{ctx: ctx, requests: make(chan *hlsFlight, 4), flights: map[int]*hlsFlight{}, cache: map[int]*hlsFragment{}}
}

// One process at a time per session. Concurrent init/fragment requests share a
// flight, while an abandoned seek cancels its process without closing the disc.
func (h *hlsPlayback) fragment(ctx context.Context, index int) (*hlsFragment, error) {
	h.mu.Lock()
	if index < 0 || index >= len(h.points)-1 {
		h.mu.Unlock()
		return nil, errors.New("播放片段不存在")
	}
	if f := h.cache[index]; f != nil {
		f.used = time.Now()
		h.mu.Unlock()
		return f, nil
	}
	f := h.flights[index]
	if f == nil || f.ctx.Err() != nil {
		child, cancel := context.WithCancel(h.ctx)
		f = &hlsFlight{index: index, done: make(chan struct{}), ctx: child, cancel: cancel}
		h.flights[index] = f
		select {
		case h.requests <- f:
		default:
			delete(h.flights, index)
			cancel()
			h.mu.Unlock()
			return nil, errors.New("播放片段请求繁忙，请重试")
		}
	}
	f.waiters++
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		f.waiters--
		if f.waiters == 0 {
			f.cancel()
		}
		h.mu.Unlock()
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-h.ctx.Done():
		return nil, h.ctx.Err()
	case <-f.done:
		return f.fragment, f.err
	}
}

func (h *hlsPlayback) finish(f *hlsFlight, fragment *hlsFragment, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.flights[f.index] == f {
		delete(h.flights, f.index)
	}
	f.fragment, f.err = fragment, err
	if err == nil {
		fragment.used = time.Now()
		if old := h.cache[f.index]; old != nil {
			h.bytes -= len(old.init) + len(old.media)
		}
		h.cache[f.index] = fragment
		h.bytes += len(fragment.init) + len(fragment.media)
		for h.bytes > hlsCacheLimit {
			oldest := -1
			for index, item := range h.cache {
				if index != f.index && (oldest < 0 || item.used.Before(h.cache[oldest].used)) {
					oldest = index
				}
			}
			if oldest < 0 {
				break
			}
			h.bytes -= len(h.cache[oldest].init) + len(h.cache[oldest].media)
			delete(h.cache, oldest)
		}
	}
	close(f.done)
}

type hlsProbe struct {
	Format struct {
		Duration string `json:"duration"`
	}
	Streams []struct {
		Index       int
		CodecType   string `json:"codec_type"`
		CodecName   string `json:"codec_name"`
		PixelFormat string `json:"pix_fmt"`
		Profile     string
		Level       int
		SampleRate  string `json:"sample_rate"`
		Packets     string `json:"nb_read_packets"`
	}
}

// Build a small access-point table using libbluray's existing navigation index.
// Authored or stale metadata can disagree with actual media duration; those discs
// retain accurate ffmpeg seeking instead of manufacturing an incorrect timeline.
func hlsPoints(ctx context.Context, native *isoNativeReader, duration float64) ([]hlsPoint, bool) {
	fallback := func() ([]hlsPoint, bool) {
		p := []hlsPoint{}
		for t := 0.0; t < duration; t += hlsSegmentSeconds {
			p = append(p, hlsPoint{time: t, offset: -1})
		}
		return append(p, hlsPoint{time: duration, offset: -1}), false
	}
	if native == nil || !native.timeSeek || math.Abs(float64(native.duration)/90000-duration) > math.Max(2, duration*0.002) {
		return fallback()
	}
	points := []hlsPoint{{time: 0, offset: 0}}
	for t := hlsSegmentSeconds; t < duration; t += hlsSegmentSeconds {
		if ctx.Err() != nil {
			return fallback()
		}
		offset, ticks, err := native.seekTime(uint64(t * 90000))
		actual := float64(ticks) / 90000
		last := points[len(points)-1]
		if err != nil || actual > t+0.1 || t-actual > 30 || offset < last.offset || actual < last.time || actual >= duration {
			return fallback()
		}
		if actual > last.time+0.01 && offset > last.offset {
			points = append(points, hlsPoint{time: actual, offset: offset})
		}
	}
	points = append(points, hlsPoint{time: duration, offset: native.size})
	// Sparse/missing CLPI points must not create enormous fragments.
	for i := 1; i < len(points); i++ {
		if points[i].time-points[i-1].time > 12 {
			return fallback()
		}
	}
	return points, true
}

func splitHLSFragment(data []byte) (*hlsFragment, error) {
	f := &hlsFragment{}
	media := false
	for len(data) > 0 {
		if len(data) < 8 {
			return nil, errPlaybackEmpty
		}
		n := uint64(binary.BigEndian.Uint32(data[:4]))
		header := uint64(8)
		if n == 1 {
			if len(data) < 16 {
				return nil, errPlaybackEmpty
			}
			n = binary.BigEndian.Uint64(data[8:16])
			header = 16
		}
		if n < header || n > uint64(len(data)) {
			return nil, errPlaybackEmpty
		}
		switch string(data[4:8]) {
		case "ftyp", "moov":
			f.init = append(f.init, data[:int(n)]...)
		case "moof", "mdat":
			f.media = append(f.media, data[:int(n)]...)
			if string(data[4:8]) == "mdat" && n > header {
				media = true
			}
		}
		data = data[int(n):]
	}
	if !media || len(f.init) == 0 || !playbackBytesReady(f.init, f.media) {
		return nil, errPlaybackEmpty
	}
	return f, nil
}
func playbackBytesReady(parts ...[]byte) bool {
	d := mp4MediaDetector{}
	for _, part := range parts {
		d.feed(part)
	}
	return d.media
}

type hlsBoundedOutput struct{ bytes.Buffer }

func (b *hlsBoundedOutput) Write(p []byte) (int, error) {
	if len(p) > hlsSegmentLimit-b.Len() {
		return 0, errors.New("播放片段超过内存上限")
	}
	return b.Buffer.Write(p)
}

func (a *App) runHLSPlayback(ctx context.Context, input string, online *isoOnlineInput, job *featureTranscode, audio int, codecs []string, forceTranscode bool, duration float64, c featurePlaybackConfig) error {
	h := job.hls
	probeCtx, probeCancel := playbackTaskTimeout(ctx, job.control, 30*time.Second)
	args := append([]string{"-v", "error"}, featureInputArgs(input)...)
	if online != nil {
		args = append(args, online.Args...)
	}
	args = append(args, "-i", input, "-show_streams", "-show_format", "-of", "json")
	var output transferLogBuffer
	command := exec.CommandContext(probeCtx, "ffprobe", args...)
	command.Stdout = &output
	err := job.control.run(probeCtx, command)
	probeCancel()
	var probe hlsProbe
	if err != nil || json.Unmarshal(output.Bytes(), &probe) != nil {
		return errors.New("无法探测光盘正片，请检查片源和读盘组件")
	}
	if duration <= 0 {
		duration, _ = strconv.ParseFloat(probe.Format.Duration, 64)
	}
	if !validHLSDuration(duration) {
		return errors.New("没有可靠的正片时长，无法建立分段时间轴")
	}
	var native *isoNativeReader
	if online != nil {
		native = online.native
	}
	h.points, h.indexed = hlsPoints(ctx, native, duration)
	supported := func(codec string) bool {
		for _, v := range codecs {
			if v == codec {
				return true
			}
		}
		return false
	}
	h.codec = "avc1.640028"
	for _, stream := range probe.Streams {
		if stream.CodecType == "audio" && (audio < 0 || stream.Index == audio) {
			h.audio = true
			break
		}
	}
	for _, stream := range probe.Streams {
		if stream.CodecType != "video" {
			continue
		}
		if !forceTranscode && h.indexed && stream.CodecName == "h264" && (stream.PixelFormat == "yuv420p" || stream.PixelFormat == "yuvj420p") {
			h.copyVideo = true
			profile := 0x64
			if strings.Contains(stream.Profile, "Baseline") {
				profile = 0x42
			}
			if stream.Profile == "Main" {
				profile = 0x4d
			}
			h.codec = fmt.Sprintf("avc1.%02x00%02x", profile, stream.Level)
		}
		if !forceTranscode && h.indexed && stream.CodecName == "hevc" && ((stream.PixelFormat == "yuv420p" && supported("hevc")) || (stream.PixelFormat == "yuv420p10le" && supported("hevc10"))) {
			h.copyVideo = true
			profile, compat := 1, 6
			if stream.PixelFormat == "yuv420p10le" {
				profile, compat = 2, 4
			}
			h.codec = fmt.Sprintf("hvc1.%d.%d.L%d.B0", profile, compat, stream.Level)
		}
		break
	}
	a.features.cacheMu.Lock()
	job.State = "segmented"
	if job.Start >= duration {
		job.Start = 0
	}
	a.features.cacheMu.Unlock()
	h.ready.Store(true)
	defer func() { h.mu.Lock(); h.cache = map[int]*hlsFragment{}; h.bytes = 0; h.mu.Unlock() }()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case f := <-h.requests:
			if f.ctx.Err() != nil {
				h.finish(f, nil, f.ctx.Err())
				continue
			}
			fragment, err := a.makeHLSFragment(f.ctx, input, online, job, h, f.index, audio, c)
			h.finish(f, fragment, err)
		}
	}
}

func validHLSDuration(duration float64) bool {
	return !math.IsNaN(duration) && !math.IsInf(duration, 0) && duration > 0 && duration <= 86400
}

func (a *App) makeHLSFragment(ctx context.Context, input string, online *isoOnlineInput, job *featureTranscode, h *hlsPlayback, index, audio int, c featurePlaybackConfig) (*hlsFragment, error) {
	start, end := h.points[index], h.points[index+1]
	seek := start.time
	silentTail := false
	if h.indexed {
		// A bounded, seekable view of this access-point interval. ffprobe/ffmpeg can
		// inspect its end without touching the end of the entire movie.
		readEnd := end.offset
		view, err := serveISOOnline(ctx, &isoJoinedReader{size: readEnd - start.offset, files: []isoStreamFile{{size: readEnd - start.offset, reader: io.NewSectionReader(online.native, start.offset, readEnd-start.offset)}}}, nil)
		if err != nil {
			return nil, err
		}
		defer view.close()
		input = view.URL
		seek = 0
		if h.audio && index >= len(h.points)-4 {
			// Some titles end their audio before the last video access point.
			// The PMT still declares audio but the interval has no audio packets.
			// Confirm that condition before padding the final video with silence.
			var tail hlsProbe
			var data transferLogBuffer
			probe := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-i", input, "-count_packets", "-show_streams", "-of", "json")
			probe.Stdout = &data
			if job.control.run(ctx, probe) == nil && json.Unmarshal(data.Bytes(), &tail) == nil {
				silentTail = true
				for _, stream := range tail.Streams {
					if stream.CodecType == "audio" && (audio < 0 || stream.Index == audio) {
						if n, _ := strconv.Atoi(stream.Packets); n > 0 {
							silentTail = false
						}
						break
					}
				}
			}
		}
	}
	args := append([]string{"-v", "error", "-nostdin"}, featureInputArgs(input)...)
	if online != nil {
		args = append(args, online.Args...)
	}
	args = append(args, "-ss", strconv.FormatFloat(seek, 'f', 6, 64), "-i", input)
	if silentTail {
		args = append(args, "-f", "lavfi", "-i", "anullsrc=r=48000:cl=stereo")
	}
	args = append(args, "-t", strconv.FormatFloat(end.time-start.time, 'f', 6, 64), "-map", "0:v:0")
	track := "0:a:0?"
	if audio >= 0 {
		track = "0:" + strconv.Itoa(audio)
	}
	if silentTail {
		track = "1:a:0"
	}
	args = append(args, "-map", track)
	if h.copyVideo {
		args = append(args, "-c:v", "copy")
		if strings.HasPrefix(h.codec, "hvc1") {
			args = append(args, "-tag:v", "hvc1")
		}
	} else {
		args = append(args, "-c:v", "libx264", "-preset", "veryfast", "-pix_fmt", "yuv420p", "-threads", strconv.Itoa(c.Threads), "-b:v", strconv.Itoa(c.Bitrate)+"k", "-vf", "scale=w='min(1920,iw)':h='min(1080,ih)':force_original_aspect_ratio=decrease:force_divisible_by=2", "-tune", "zerolatency", "-force_key_frames", "expr:gte(t,n_forced*2)")
	}
	args = append(args, "-c:a", "aac", "-ar", "48000", "-ac", "2", "-b:a", "192k", "-movflags", "+frag_keyframe+empty_moov+default_base_moof", "-f", "mp4", "pipe:1")
	command := exec.CommandContext(ctx, "ffmpeg", args...)
	var output hlsBoundedOutput
	command.Stdout = &output
	if err := job.control.run(ctx, command); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("播放片段生成失败，请检查音轨和解码能力")
	}
	return splitHLSFragment(output.Bytes())
}

func (a *App) featureHLS(w http.ResponseWriter, r *http.Request, user User) {
	if !featureMethod(w, r, http.MethodGet, http.MethodHead) {
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/features/hls/"), "/")
	if len(parts) != 2 || len(parts[0]) != 40 || strings.Trim(parts[0], "0123456789abcdef") != "" {
		http.NotFound(w, r)
		return
	}
	a.features.cacheMu.Lock()
	job := a.features.transcodes[parts[0]]
	if job == nil || !job.HLS || job.deleting || (job.Owner != user.ID && !user.Admin) {
		a.features.cacheMu.Unlock()
		http.NotFound(w, r)
		return
	}
	item, h, done, errorText := job.Item, job.hls, job.Done, job.Error
	job.Used = time.Now()
	job.heartbeat = job.Used
	a.features.cacheMu.Unlock()
	if _, err := a.featureAccessibleItem(user, item); err != nil {
		featureError(w, err)
		return
	}
	if !a.canPlay(user.ID) {
		fail(w, 403, "该账号没有播放权限")
		return
	}
	if done || h == nil {
		fail(w, 410, "分段播放已结束，请重新播放")
		return
	}
	if errorText != "" {
		fail(w, 502, errorText)
		return
	}
	if !h.ready.Load() {
		fail(w, 503, "正在准备分段播放")
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	suffix := ""
	if token := r.URL.Query().Get("api_key"); token != "" {
		suffix = "?api_key=" + url.QueryEscape(token)
	}
	if parts[1] == "master.m3u8" {
		codecs := h.codec
		if h.audio {
			codecs += ",mp4a.40.2"
		}
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		if r.Method != http.MethodHead {
			fmt.Fprintf(w, "#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-STREAM-INF:BANDWIDTH=20000000,CODECS=\"%s\"\nindex.m3u8%s\n", codecs, suffix)
		}
		return
	}
	if parts[1] == "index.m3u8" {
		var playlist strings.Builder
		target := 0.0
		for i := 1; i < len(h.points); i++ {
			target = math.Max(target, h.points[i].time-h.points[i-1].time)
		}
		fmt.Fprintf(&playlist, "#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-PLAYLIST-TYPE:VOD\n#EXT-X-TARGETDURATION:%d\n#EXT-X-MEDIA-SEQUENCE:0\n", int(math.Ceil(target)))
		for i := 0; i < len(h.points)-1; i++ {
			if i > 0 {
				playlist.WriteString("#EXT-X-DISCONTINUITY\n")
			}
			fmt.Fprintf(&playlist, "#EXT-X-MAP:URI=\"init-%d.mp4%s\"\n#EXTINF:%.6f,\nsegment-%d.m4s%s\n", i, suffix, h.points[i+1].time-h.points[i].time, i, suffix)
		}
		playlist.WriteString("#EXT-X-ENDLIST\n")
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		if r.Method != http.MethodHead {
			io.WriteString(w, playlist.String())
		}
		return
	}
	name := parts[1]
	init := strings.HasPrefix(name, "init-") && strings.HasSuffix(name, ".mp4")
	if !init && !(strings.HasPrefix(name, "segment-") && strings.HasSuffix(name, ".m4s")) {
		http.NotFound(w, r)
		return
	}
	number := strings.TrimSuffix(strings.TrimPrefix(name, "segment-"), ".m4s")
	if init {
		number = strings.TrimSuffix(strings.TrimPrefix(name, "init-"), ".mp4")
	}
	index, err := strconv.Atoi(number)
	if err != nil || index < 0 || index >= len(h.points)-1 || number != strconv.Itoa(index) {
		http.NotFound(w, r)
		return
	}
	if r.Method == http.MethodHead {
		w.Header().Set("Content-Type", "video/mp4")
		return
	}
	fragment, err := h.fragment(r.Context(), index)
	if err != nil {
		if r.Context().Err() == nil {
			fail(w, 502, err.Error())
		}
		return
	}
	data := fragment.media
	if init {
		data = fragment.init
	}
	w.Header().Set("Content-Type", "video/mp4")
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(30 * time.Second))
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(data))
}
