package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"strconv"
	"strings"
	"time"
)

var transferHTTP = &http.Client{
	Transport: &http.Transport{Proxy: nil, DisableCompression: true, TLSHandshakeTimeout: 15 * time.Second,
		IdleConnTimeout: 90 * time.Second, MaxIdleConnsPerHost: 8,
		DialContext: (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext},
	CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
}

type transferObject struct {
	Sign     string `json:"sign"`
	IsDir    bool   `json:"is_dir"`
	Size     int64  `json:"size"`
	Modified string `json:"modified"`
}

func transferCloudObject(ctx context.Context, mount cloudMount, remote string) (transferObject, error) {
	var object transferObject
	err := cloudCall(ctx, "POST", "/api/fs/get", M{"path": cloudStoragePath(mount, remote)}, &object, "AI-Emby-Transfer")
	if err == nil && object.IsDir {
		err = errors.New("目标应为视频文件，不能是文件夹")
	}
	return object, err
}

type transferProgressReader struct {
	reader io.Reader
	read   int64
	update func(int64)
}

func (reader *transferProgressReader) Read(data []byte) (int, error) {
	n, err := reader.reader.Read(data)
	reader.read += int64(n)
	reader.update(reader.read)
	return n, err
}

func transferRegular(name string, size int64) bool {
	info, err := os.Lstat(name)
	return err == nil && info.Mode().IsRegular() && (size <= 0 || info.Size() == size)
}

func (run *cloudTransferRun) download(index int, mount cloudMount, object transferObject) error {
	run.mu.Lock()
	file := run.task.Files[index]
	run.mu.Unlock()
	source := transferLocal(run.task.ID, file.ID, ".source")
	if file.Downloaded && transferRegular(source, file.SourceSize) && (file.SourceStamp == "" || file.SourceStamp == object.Modified) {
		return nil
	}
	if file.Downloaded || file.Remuxed {
		return errors.New("已下载文件或网盘源文件发生变化，保留临时文件供检查，请重新创建任务")
	}
	if err := transferStage(run.ctx, run, index, "download"); err != nil {
		return err
	}
	partial := transferLocal(run.task.ID, file.ID, ".download")
	var offset int64
	if info, err := os.Lstat(partial); err == nil {
		if !info.Mode().IsRegular() {
			return errors.New("下载临时文件不是普通文件")
		}
		if file.SourceStamp == object.Modified && (object.Modified != "" || file.DownloadETag != "") && info.Size() < file.SourceSize {
			offset = info.Size()
		}
	}
	if object.Sign == "" {
		return errors.New("源网盘未返回可下载凭据，请检查账号和文件权限")
	}
	endpoint, _ := url.Parse(cloudEngineURL)
	endpoint.Path = "/p" + cloudStoragePath(mount, file.Remote)
	endpoint.RawQuery = url.Values{"sign": {object.Sign}, "d": {"1"}}.Encode()
	request, err := http.NewRequestWithContext(run.ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return errCloudEngine
	}
	request.Header.Set("User-Agent", "AI-Emby-Transfer")
	request.Header.Set("Accept-Encoding", "identity")
	if offset > 0 {
		request.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
		if file.DownloadETag != "" {
			request.Header.Set("If-Range", file.DownloadETag)
		}
	}
	response, err := transferHTTP.Do(request)
	if err != nil {
		return errors.New("视频下载中断，请检查网盘连接后恢复任务")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusPartialContent {
		return fmt.Errorf("网盘下载返回 HTTP %d，请检查账号、下载权限和挂载配置", response.StatusCode)
	}
	if offset > 0 && response.StatusCode == http.StatusOK {
		offset = 0
	}
	if offset > 0 && file.DownloadETag != "" && response.Header.Get("ETag") != "" && file.DownloadETag != response.Header.Get("ETag") {
		return errors.New("网盘源文件的版本发生变化，已停止续传并保留副本，请重新创建任务")
	}
	if response.StatusCode == http.StatusPartialContent {
		var start, end, total int64
		if _, err = fmt.Sscanf(response.Header.Get("Content-Range"), "bytes %d-%d/%d", &start, &end, &total); err != nil || start != offset || total != file.SourceSize || end != file.SourceSize-1 {
			return errors.New("下载续传范围不一致，临时文件已保留，请刷新挂载后重试")
		}
	}
	if response.ContentLength >= 0 && response.ContentLength != file.SourceSize-offset {
		return errors.New("网盘返回的下载大小与目录记录不一致，已停止下载")
	}
	if err = run.change(index, func(saved *cloudTransferFile) {
		saved.SourceStamp, saved.DownloadETag = object.Modified, response.Header.Get("ETag")
	}); err != nil {
		return err
	}
	flags := os.O_CREATE | os.O_WRONLY
	if offset == 0 {
		flags |= os.O_TRUNC
	} else {
		flags |= os.O_APPEND
	}
	output, err := os.OpenFile(partial, flags, 0o600)
	if err != nil {
		return errors.New("无法写入下载文件，请检查临时目录权限和磁盘空间")
	}
	reader := &transferProgressReader{reader: io.LimitReader(response.Body, file.SourceSize-offset+1), update: func(bytes int64) { run.progress(index, offset+bytes) }}
	count, copyErr := io.Copy(output, reader)
	if copyErr == nil {
		copyErr = output.Sync()
	}
	closeErr := output.Close()
	if copyErr != nil || closeErr != nil {
		return errors.New("下载未完成，临时文件已保留，下次恢复时继续下载")
	}
	if count+offset != file.SourceSize {
		return errors.New("视频下载大小不完整，已保留临时文件，未进行重封装或上传")
	}
	if err = os.Rename(partial, source); err != nil {
		return errors.New("保存完整下载文件失败，请检查临时目录")
	}
	return run.change(index, func(file *cloudTransferFile) { file.Downloaded = true })
}

type transferProbeData struct {
	Streams []struct {
		CodecType string `json:"codec_type"`
		CodecName string `json:"codec_name"`
	} `json:"streams"`
	Chapters []struct {
		Start string `json:"start_time"`
		End   string `json:"end_time"`
	} `json:"chapters"`
	Format struct {
		Duration string `json:"duration"`
	} `json:"format"`
}

type transferLogBuffer struct{ bytes.Buffer }

func (buffer *transferLogBuffer) Write(data []byte) (int, error) {
	n := len(data)
	if buffer.Len() < 4<<20 {
		_, _ = buffer.Buffer.Write(data[:min(len(data), (4<<20)-buffer.Len())])
	}
	return n, nil
}

func transferProbe(ctx context.Context, file string) (transferProbeData, error) {
	var result transferProbeData
	command := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-show_streams", "-show_chapters", "-show_format", "-of", "json", file)
	var output transferLogBuffer
	command.Stdout = &output
	if command.Run() != nil || json.Unmarshal(output.Bytes(), &result) != nil || len(result.Streams) == 0 {
		return result, errors.New("无法读取完整音视频流；请检查源文件，ISO 镜像需先提取视频")
	}
	return result, nil
}

func transferVerifyStreams(source, output transferProbeData) error {
	if len(source.Streams) != len(output.Streams) || len(source.Chapters) != len(output.Chapters) {
		return errors.New("重封装前后音视频流、附件或章节数量不一致，未上传")
	}
	for index, stream := range source.Streams {
		if stream != output.Streams[index] {
			return errors.New("重封装前后编码或流类型不一致，未上传")
		}
	}
	for index, chapter := range source.Chapters {
		for _, pair := range [][2]string{{chapter.Start, output.Chapters[index].Start}, {chapter.End, output.Chapters[index].End}} {
			left, _ := strconv.ParseFloat(pair[0], 64)
			right, _ := strconv.ParseFloat(pair[1], 64)
			if math.Abs(left-right) > 0.2 {
				return errors.New("重封装后的章节位置发生变化，未上传")
			}
		}
	}
	left, _ := strconv.ParseFloat(source.Format.Duration, 64)
	right, _ := strconv.ParseFloat(output.Format.Duration, 64)
	if left > 0 && right > 0 && math.Abs(left-right) > max(2, left*0.01) {
		return errors.New("重封装前后时长差异过大，未上传")
	}
	return nil
}

func transferHash(ctx context.Context, name string) (string, error) {
	file, err := os.Open(name)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	buffer := make([]byte, 1<<20)
	for {
		if err = ctx.Err(); err != nil {
			return "", err
		}
		n, readErr := file.Read(buffer)
		_, _ = hash.Write(buffer[:n])
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return "", readErr
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func (run *cloudTransferRun) remux(index int) error {
	run.mu.Lock()
	file := run.task.Files[index]
	run.mu.Unlock()
	output := transferLocal(run.task.ID, file.ID, ".output")
	if file.Remuxed && transferRegular(output, file.OutputSize) {
		hash, err := transferHash(run.ctx, output)
		if err != nil || hash != file.OutputSHA256 {
			return errors.New("已处理文件的校验值发生变化，保留副本供检查，未上传")
		}
		return nil
	}
	if file.Remuxed {
		return errors.New("已处理文件丢失或大小改变，请检查临时目录")
	}
	if err := transferStage(run.ctx, run, index, "remux"); err != nil {
		return err
	}
	source := transferLocal(run.task.ID, file.ID, ".source")
	before, err := transferProbe(run.ctx, source)
	if err != nil {
		return err
	}
	partial := transferLocal(run.task.ID, file.ID, ".remux")
	format := "matroska"
	if run.task.Config.Format == "mp4" {
		format = "mp4"
	}
	ctx, cancel := context.WithCancel(run.ctx)
	defer cancel()
	command := exec.CommandContext(ctx, "ffmpeg", "-nostdin", "-hide_banner", "-v", "error", "-y", "-i", source,
		"-map", "0", "-c", "copy", "-map_metadata", "0", "-map_chapters", "0", "-progress", "pipe:1", "-f", format, partial)
	var stderr transferLogBuffer
	command.Stderr = &stderr
	stdout, err := command.StdoutPipe()
	if err != nil || command.Start() != nil {
		return errors.New("无法启动无损重封装程序，请检查服务器的 FFmpeg")
	}
	finished := make(chan struct{})
	spaceError := make(chan error, 1)
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-finished:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				if info, err := os.Stat(partial); err == nil {
					run.progress(index, info.Size())
					if info.Size() > transferOutputAllowance(file.SourceSize) {
						spaceError <- errors.New("重封装输出超出预留空间，已停止并保留下载副本")
						cancel()
						return
					}
				}
			}
		}
	}()
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		if value, ok := strings.CutPrefix(scanner.Text(), "total_size="); ok {
			if size, err := strconv.ParseInt(value, 10, 64); err == nil {
				run.progress(index, size)
			}
		}
	}
	err = command.Wait()
	close(finished)
	select {
	case e := <-spaceError:
		return e
	default:
	}
	if err != nil {
		return errors.New("无损重封装失败；此容器可能不支持源文件中的编码、字幕或数据流，下载副本已保留，可改用 MKV 后重新创建任务")
	}
	after, err := transferProbe(run.ctx, partial)
	if err != nil {
		return err
	}
	if err = transferVerifyStreams(before, after); err != nil {
		return err
	}
	info, err := os.Stat(partial)
	if err != nil || info.Size() <= 0 || info.Size() > transferOutputAllowance(file.SourceSize) {
		return errors.New("重封装输出为空或超出预留大小，未上传")
	}
	hash, err := transferHash(run.ctx, partial)
	if err != nil {
		return err
	}
	if err = os.Rename(partial, output); err != nil {
		return errors.New("保存处理后的视频失败，请检查临时目录")
	}
	return run.change(index, func(file *cloudTransferFile) {
		file.Remuxed, file.OutputSize, file.OutputSHA256 = true, info.Size(), hash
	})
}

func transferMkdir(ctx context.Context, mount cloudMount, directory string) error {
	cloudManageMu.Lock()
	defer cloudManageMu.Unlock()
	current := "/"
	for _, segment := range strings.Split(strings.Trim(directory, "/"), "/") {
		if segment == "" {
			continue
		}
		found := false
		for page := 1; page <= 1000; page++ {
			list, err := cloudList(ctx, mount, current, page, true)
			if err != nil {
				return err
			}
			for _, file := range list.Content {
				if file.Name == segment {
					if !file.IsDir {
						return errors.New("上传路径存在同名文件，请更换父目录")
					}
					found = true
					break
				}
			}
			if found || page*200 >= list.Total || len(list.Content) < 200 {
				break
			}
		}
		current = path.Join(current, segment)
		if !found {
			if err := cloudCall(ctx, "POST", "/api/fs/mkdir", M{"path": cloudStoragePath(mount, current)}, nil, ""); err != nil {
				return err
			}
		}
	}
	return nil
}

func transferUploaded(ctx context.Context, mount cloudMount, remote string, size int64) (bool, error) {
	for page := 1; page <= 1000; page++ {
		list, err := cloudList(ctx, mount, path.Dir(remote), page, true)
		if err != nil {
			return false, err
		}
		for _, file := range list.Content {
			if file.Name == path.Base(remote) {
				if file.IsDir || file.Size != size {
					return false, errors.New("上传目标存在大小不一致的同名文件，未覆盖")
				}
				return true, nil
			}
		}
		if page*200 >= list.Total || len(list.Content) < 200 {
			return false, nil
		}
	}
	return false, errors.New("上传目录文件过多，请选择更小的父目录")
}

func (run *cloudTransferRun) upload(index int, target cloudTransferTarget) error {
	run.mu.Lock()
	file := run.task.Files[index]
	uploadState := file.Uploads[target.MountID]
	run.mu.Unlock()
	if uploadState == "complete" {
		return nil
	}
	mount, err := run.app.cloudMount(target.MountID)
	if err != nil || !mount.Enabled {
		return errors.New("上传目标已移除或暂停，请恢复挂载后重试")
	}
	if err = transferStage(run.ctx, run, index, "upload"); err != nil {
		return err
	}
	remote := path.Join(target.Path, run.task.Folder, file.Output)
	if err = transferMkdir(run.ctx, mount, path.Dir(remote)); err != nil {
		return err
	}
	exists, err := transferUploaded(run.ctx, mount, remote, file.OutputSize)
	if err != nil {
		return err
	}
	if exists && uploadState != "pending" {
		return errors.New("上传目录存在未经本任务确认的同名文件，未覆盖")
	}
	if !exists {
		if err = run.change(index, func(file *cloudTransferFile) {
			if file.Uploads == nil {
				file.Uploads = map[string]string{}
			}
			file.Uploads[target.MountID] = "pending"
		}); err != nil {
			return err
		}
		if err = cloudCall(run.ctx, http.MethodGet, "/api/me", nil, nil, ""); err != nil {
			return err
		}
		cloudClient.Lock()
		token := cloudClient.token
		cloudClient.Unlock()
		input, err := os.Open(transferLocal(run.task.ID, file.ID, ".output"))
		if err != nil {
			return errors.New("待上传的处理文件不存在")
		}
		defer input.Close()
		reader := &transferProgressReader{reader: input, update: func(bytes int64) { run.progress(index, bytes) }}
		request, err := http.NewRequestWithContext(run.ctx, http.MethodPut, cloudEngineURL+"/api/fs/put", reader)
		if err != nil {
			return errCloudEngine
		}
		request.ContentLength = file.OutputSize
		request.Header.Set("Authorization", token)
		request.Header.Set("File-Path", url.PathEscape(cloudStoragePath(mount, remote)))
		request.Header.Set("As-Task", "false")
		request.Header.Set("Overwrite", "false")
		request.Header.Set("Content-Type", "application/octet-stream")
		request.Header.Set("X-File-Sha256", file.OutputSHA256)
		response, err := transferHTTP.Do(request)
		if err != nil {
			if run.ctx.Err() != nil {
				return run.ctx.Err()
			}
			return errors.New("上传连接中断，处理后的视频已保留；恢复时先核对网盘结果")
		}
		var envelope cloudEnvelope
		decodeErr := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&envelope)
		response.Body.Close()
		if decodeErr != nil || response.StatusCode != http.StatusOK || envelope.Code != 200 {
			return errors.New("网盘上传失败，请检查账号、可用空间和上传权限；本地处理副本已保留")
		}
		if len(envelope.Data) > 0 && strings.Contains(string(envelope.Data), `"task"`) {
			return errors.New("网盘引擎返回了后台上传任务，需等待目标文件完整出现后恢复确认")
		}
		for {
			exists, err = transferUploaded(run.ctx, mount, remote, file.OutputSize)
			if err != nil || exists {
				break
			}
			if err = trackingPause(run.ctx, 2*time.Second); err != nil {
				return err
			}
		}
		if err != nil {
			return err
		}
	}
	return run.change(index, func(file *cloudTransferFile) { file.Uploads[target.MountID] = "complete" })
}
