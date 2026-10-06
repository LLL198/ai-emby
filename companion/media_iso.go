package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const isoHeaderSize = 64 << 10

type isoMediaInfo struct {
	Container string
	Size      int64
}

// The original cloud filename may live in a resolver query instead of its URL path.
func isoMediaCandidate(x Item) bool {
	if strings.EqualFold(filepath.Ext(x.Path), ".iso") {
		return true
	}
	u, err := url.Parse(x.URL)
	return err == nil && (strings.EqualFold(filepath.Ext(u.Path), ".iso") ||
		strings.EqualFold(filepath.Ext(u.Query().Get("path")), ".iso"))
}

func mediaHeaderContainer(data []byte) string {
	switch {
	case bytes.HasPrefix(data, []byte{0x1a, 0x45, 0xdf, 0xa3}):
		if bytes.Contains(data[:min(len(data), 4096)], []byte("webm")) {
			return "webm"
		}
		return "mkv"
	case len(data) >= 12 && string(data[4:8]) == "ftyp":
		return "mp4"
	case len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "AVI ":
		return "avi"
	case len(data) > 376 && data[0] == 0x47 && data[188] == 0x47 && data[376] == 0x47:
		return "ts"
	case len(data) > 388 && data[4] == 0x47 && data[196] == 0x47 && data[388] == 0x47:
		return "m2ts"
	case bytes.HasPrefix(data, []byte{0, 0, 1, 0xba}):
		return "mpg"
	}
	for sector := 16; sector < 32 && sector*2048+6 <= len(data); sector++ {
		switch string(data[sector*2048+1 : sector*2048+6]) {
		case "CD001", "NSR02", "NSR03", "BEA01":
			return "iso"
		}
	}
	return "unknown"
}

var isoHTTP = &http.Client{Transport: &http.Transport{
	Proxy: http.ProxyFromEnvironment, DisableCompression: true,
	ResponseHeaderTimeout: 30 * time.Second, TLSHandshakeTimeout: 15 * time.Second,
}}

func isoSourceRequest(ctx context.Context, input, rangeValue string) (*http.Response, error) {
	mapped, host := localMappedSource(ctx, input)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, mapped, nil)
	if err != nil {
		return nil, errors.New("媒体地址无效")
	}
	if host != "" {
		req.Host = host
	}
	req.Header.Set("User-Agent", "AI-Emby-ISO/1.0")
	req.Header.Set("Accept-Encoding", "identity")
	if rangeValue != "" {
		req.Header.Set("Range", rangeValue)
	}
	response, err := isoHTTP.Do(req)
	if err != nil {
		return nil, errors.New("无法读取 ISO 媒体源，请检查网盘连接")
	}
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusPartialContent {
		response.Body.Close()
		return nil, fmt.Errorf("ISO 媒体源返回 HTTP %d", response.StatusCode)
	}
	return response, nil
}

func inspectISOInput(ctx context.Context, input string) (isoMediaInfo, error) {
	if strings.HasPrefix(input, "bluray:") {
		input = strings.TrimPrefix(input, "bluray:")
	}
	var reader io.ReadCloser
	var size int64
	if strings.HasPrefix(input, "http") {
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		response, err := isoSourceRequest(ctx, input, "bytes=0-65535")
		if err != nil {
			return isoMediaInfo{}, err
		}
		reader, size = response.Body, response.ContentLength
		if response.StatusCode == http.StatusPartialContent {
			var start, end, total int64
			if _, err := fmt.Sscanf(response.Header.Get("Content-Range"), "bytes %d-%d/%d", &start, &end, &total); err != nil || start != 0 || end < start || end >= isoHeaderSize || total <= end {
				reader.Close()
				return isoMediaInfo{}, errors.New("ISO 媒体源返回了不正确的文件范围")
			}
			size = total
		}
	} else {
		file, err := os.Open(input)
		if err != nil {
			return isoMediaInfo{}, err
		}
		reader = file
		if info, err := file.Stat(); err == nil {
			size = info.Size()
		}
	}
	defer reader.Close()
	data, err := io.ReadAll(io.LimitReader(reader, isoHeaderSize))
	if err != nil {
		return isoMediaInfo{}, errors.New("读取 ISO 文件头失败")
	}
	return isoMediaInfo{Container: mediaHeaderContainer(data), Size: size}, nil
}

func (a *App) featureInspectPlayback(w http.ResponseWriter, r *http.Request, user User) {
	if !featureMethod(w, r, http.MethodGet) {
		return
	}
	if user.API || !a.canPlay(user.ID) {
		fail(w, 403, "需要有播放权限的登录账号")
		return
	}
	x, err := a.featureAccessibleItem(user, r.URL.Query().Get("ID"))
	if err != nil {
		featureError(w, err)
		return
	}
	if !isoMediaCandidate(x) {
		respond(w, M{"Container": "", "IsDisc": false})
		return
	}
	input, err := a.featureRawMediaInput(x)
	if err != nil {
		fail(w, 422, "媒体源不可读取")
		return
	}
	info, err := inspectISOInput(r.Context(), input)
	if err != nil {
		fail(w, 502, err.Error())
		return
	}
	// Only classification is public. Source URLs and internal task credentials stay private.
	respond(w, M{"Container": info.Container, "Size": info.Size, "IsDisc": info.Container == "iso"})
}

// Keep a complete image in the existing playback cache. Partial downloads are never reusable.
func downloadISO(ctx context.Context, input, target string, expected, available int64, progress func(int64, int64)) error {
	return downloadISOControlled(ctx, input, target, expected, available, progress, nil)
}

func downloadISOControlled(ctx context.Context, input, target string, expected, available int64, progress func(int64, int64), control *playbackTaskControl) error {
	if err := control.wait(ctx); err != nil {
		return err
	}
	if info, err := os.Lstat(target); err == nil && info.Mode().IsRegular() && expected > 0 && info.Size() == expected {
		progress(expected, expected)
		return nil
	}
	if available <= 0 || expected > available {
		return errors.New("ISO 超过可用播放缓存，请在播放管理中增大缓存上限或清理缓存")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	// A large disc has no total transfer timeout, but stalled reads stop after 90 seconds.
	var idle *time.Timer
	idle = time.AfterFunc(90*time.Second, func() {
		if control.isPaused() {
			idle.Reset(90 * time.Second)
		} else {
			cancel()
		}
	})
	defer idle.Stop()
	response, err := isoSourceRequest(ctx, input, "")
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return errors.New("ISO 缓存需要完整文件，媒体源返回了部分内容")
	}
	if response.ContentLength > available || (expected > 0 && response.ContentLength >= 0 && response.ContentLength != expected) {
		return errors.New("ISO 大小发生变化或超过可用播放缓存")
	}
	if expected <= 0 {
		expected = response.ContentLength
	}
	partial := target + ".partial"
	file, err := os.OpenFile(partial, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return errors.New("无法写入 ISO 缓存，请检查磁盘空间")
	}
	defer os.Remove(partial)
	reader := &transferProgressReader{reader: &playbackControlledReader{ctx: ctx, reader: io.LimitReader(response.Body, available+1), control: control}, update: func(n int64) {
		idle.Reset(90 * time.Second)
		progress(n, expected)
	}}
	n, copyErr := io.Copy(file, reader)
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil || (expected > 0 && n != expected) || n > available {
		return errors.New("ISO 缓存未完成，请检查连接、磁盘空间或缓存上限后重试")
	}
	return os.Rename(partial, target)
}

type isoArchiveFile struct {
	Path string
	Size int64
}

// Read the archive's technical listing, without extracting arbitrary paths.
func isoArchiveFiles(data string) []isoArchiveFile {
	var result []isoArchiveFile
	var entry isoArchiveFile
	flush := func() {
		if entry.Path != "" && entry.Size > 0 {
			result = append(result, entry)
		}
		entry = isoArchiveFile{}
	}
	for _, line := range strings.Split(strings.ReplaceAll(data, "\r", ""), "\n") {
		if line == "" {
			flush()
		} else if strings.HasPrefix(line, "Path = ") {
			entry.Path = strings.TrimPrefix(line, "Path = ")
		} else if strings.HasPrefix(line, "Size = ") {
			entry.Size, _ = strconv.ParseInt(strings.TrimPrefix(line, "Size = "), 10, 64)
		}
	}
	flush()
	return result
}

var dvdTitleFile = regexp.MustCompile(`(?i)^VIDEO_TS/VTS_([0-9]{2})_([1-9])\.VOB$`)

func isoMainFiles(files []isoArchiveFile) ([]isoArchiveFile, bool) {
	groups := map[string][]isoArchiveFile{}
	var regular []isoArchiveFile
	for _, file := range files {
		name := strings.ReplaceAll(file.Path, "\\", "/")
		if strings.HasPrefix(name, "/") || strings.Contains(name, "..") || strings.ContainsAny(name, "\r\n|:") {
			continue
		}
		if match := dvdTitleFile.FindStringSubmatch(name); match != nil {
			groups[match[1]] = append(groups[match[1]], file)
		} else if strings.Contains(strings.ToUpper(name), "BDMV/") {
			// libbluray chooses and joins a playlist instead of guessing its biggest clip.
			return nil, true
		} else {
			switch strings.ToLower(filepath.Ext(name)) {
			case ".mkv", ".mp4", ".avi", ".ts", ".m2ts", ".mpg", ".mpeg", ".mov", ".webm":
				regular = append(regular, file)
			}
		}
	}
	var selected []isoArchiveFile
	var biggest int64
	for _, group := range groups {
		var size int64
		for _, file := range group {
			size += file.Size
		}
		if size > biggest {
			biggest, selected = size, group
		}
	}
	if len(selected) > 0 {
		sort.Slice(selected, func(i, j int) bool { return selected[i].Path < selected[j].Path })
		return selected, false
	}
	for _, file := range regular {
		if file.Size > biggest {
			biggest, selected = file.Size, []isoArchiveFile{file}
		}
	}
	return selected, false
}

func (a *App) prepareISOPlayback(ctx context.Context, input, dir string, info isoMediaInfo, job *featureTranscode) (string, error) {
	// Serialize image preparation so concurrent downloads cannot both claim the same free cache budget.
	a.features.isoMu.Lock()
	defer a.features.isoMu.Unlock()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	update := func(state string, done, total int64) {
		a.features.cacheMu.Lock()
		job.State, job.Downloaded, job.Total = state, done, total
		a.features.cacheMu.Unlock()
	}
	limit := int64(a.featurePlaybackConfig().CacheGB) << 30
	image := input
	if strings.HasPrefix(input, "http") {
		update("caching", 0, info.Size)
		image = filepath.Join(dir, "source.iso")
		available := limit - featureDirectorySize(filepath.Join(featureDataRoot(), "playback"))
		if err := downloadISOControlled(ctx, input, image, info.Size, available, func(done, total int64) { update("caching", done, total) }, job.control); err != nil {
			return "", err
		}
	}
	update("reading-disc", 0, 0)
	tool, err := exec.LookPath("7z")
	if err != nil {
		tool, err = exec.LookPath("7zz")
	}
	if err != nil {
		return "", errors.New("光盘读取工具尚未安装，请更新 Docker 镜像")
	}
	var listing transferLogBuffer
	command := exec.CommandContext(ctx, tool, "l", "-slt", "-bd", "-p-", "--", image)
	command.Stdout = &listing
	if err := job.control.run(ctx, command); err != nil {
		return "", errors.New("无法读取光盘镜像，文件可能损坏、加密或格式不受支持")
	}
	files, bluray := isoMainFiles(isoArchiveFiles(listing.String()))
	if bluray {
		return "bluray:" + image, nil
	}
	if len(files) == 0 {
		return "", errors.New("光盘中没有找到可播放的正片视频")
	}
	output := filepath.Join(dir, "disc-video")
	paths := make([]string, 0, len(files))
	var bytesNeeded int64
	complete := true
	for _, file := range files {
		path := filepath.Join(output, filepath.Base(strings.ReplaceAll(file.Path, "\\", "/")))
		paths = append(paths, path)
		if transferRegular(path, file.Size) {
			continue
		}
		complete = false
		additional := file.Size
		if cached, err := os.Lstat(path); err == nil && cached.Mode().IsRegular() {
			additional = max(0, file.Size-cached.Size())
		}
		if additional > limit-bytesNeeded {
			return "", errors.New("光盘正片超过播放缓存上限")
		}
		bytesNeeded += additional
	}
	if bytesNeeded > limit-featureDirectorySize(filepath.Join(featureDataRoot(), "playback")) {
		return "", errors.New("正片提取需要更多播放缓存，请增大缓存上限或清理缓存")
	}
	if err := os.MkdirAll(output, 0700); err != nil {
		return "", err
	}
	args := []string{"e", "-y", "-bd", "-bb0", "-p-", "-o" + output, "--", image}
	for _, file := range files {
		args = append(args, file.Path)
	}
	if !complete {
		if err := job.control.run(ctx, exec.CommandContext(ctx, tool, args...)); err != nil {
			return "", errors.New("光盘正片提取失败，请检查镜像或缓存空间")
		}
	}
	for i, file := range files {
		if !transferRegular(paths[i], file.Size) {
			return "", errors.New("光盘正片不完整，已停止播放")
		}
	}
	if len(paths) == 1 {
		return paths[0], nil
	}
	return "concat:" + strings.Join(paths, "|"), nil
}

func (a *App) featurePlaybackStatus(w http.ResponseWriter, r *http.Request, user User) {
	if !featureMethod(w, r, http.MethodGet) {
		return
	}
	key := r.URL.Query().Get("ID")
	a.features.cacheMu.Lock()
	job := a.features.transcodes[key]
	var record featureTranscode
	if job != nil && (job.Owner == user.ID || user.Admin) && !user.API {
		record = *job
	}
	a.features.cacheMu.Unlock()
	if job == nil || (record.Owner != user.ID && !user.Admin) || user.API {
		fail(w, 404, "播放任务不存在")
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
	a.features.cacheMu.Lock()
	if a.features.transcodes[key] == job {
		job.Used = time.Now()
		job.heartbeat = job.Used
	}
	a.features.cacheMu.Unlock()
	ready := false
	if record.stream != nil {
		ready = record.stream.ready.Load()
	} else {
		ready = playbackFileReady(filepath.Join(record.Directory, "video.mp4"))
	}
	respond(w, M{"State": record.State, "SourceMode": record.SourceMode, "FallbackReason": record.FallbackReason, "Paused": record.Paused, "Downloaded": record.Downloaded, "Total": record.Total, "Ready": ready, "Done": record.Done, "Error": record.Error, "Start": record.Start, "Stream": record.Stream})
}
