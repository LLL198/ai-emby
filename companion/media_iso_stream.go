package main

import (
	"container/list"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golift.io/udf"
)

const (
	isoRangeBlock      = 256 << 10
	isoStreamBlock     = 4 << 20
	isoRangeCacheBytes = 8 << 20
)

var errISORangeUnavailable = errors.New("镜像源不支持可靠的分段读取")

// A small memory cache joins sector reads into bounded HTTP ranges. No image is written to disk.
type isoRangeReader struct {
	ctx        context.Context
	input      string
	local      io.ReaderAt
	size       int64
	mu         sync.Mutex
	blocks     map[int64]*list.Element
	order      list.List
	downloaded int64
	metadata   bool
	control    *playbackTaskControl
}

type isoRangeCacheBlock struct {
	offset int64
	data   []byte
}

func newISORangeReader(ctx context.Context, input string, size int64) *isoRangeReader {
	return &isoRangeReader{ctx: ctx, input: input, size: size, blocks: map[int64]*list.Element{}, metadata: true}
}

func (reader *isoRangeReader) Size() int64 { return reader.size }

func (reader *isoRangeReader) blockSize() int64 {
	if reader.metadata {
		return isoRangeBlock
	}
	return isoStreamBlock
}

func (reader *isoRangeReader) startStreaming(ctx context.Context) {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	reader.metadata, reader.ctx = false, ctx
	// The same offset now identifies a larger block. Never reuse a short metadata block.
	reader.blocks = map[int64]*list.Element{}
	reader.order.Init()
}

func (reader *isoRangeReader) block(offset int64) ([]byte, error) {
	if cached := reader.blocks[offset]; cached != nil {
		reader.order.MoveToFront(cached)
		return cached.Value.(isoRangeCacheBlock).data, nil
	}
	length := min(reader.blockSize(), reader.size-offset)
	if length <= 0 {
		return nil, io.EOF
	}
	if reader.metadata && reader.downloaded+length > 8<<20 {
		return nil, errors.New("镜像目录过大，无法在线读取")
	}
	data := make([]byte, length)
	if reader.local != nil {
		if _, err := io.ReadFull(io.NewSectionReader(reader.local, offset, length), data); err != nil {
			return nil, errors.New("本地镜像分段读取未完成")
		}
	} else {
		ctx, cancel := context.WithTimeout(reader.ctx, 30*time.Second)
		defer cancel()
		response, err := isoSourceRequest(ctx, reader.input, fmt.Sprintf("bytes=%d-%d", offset, offset+length-1))
		if err != nil {
			return nil, err
		}
		defer response.Body.Close()
		var start, end, total int64
		if response.StatusCode != http.StatusPartialContent {
			return nil, errISORangeUnavailable
		}
		if _, err := fmt.Sscanf(response.Header.Get("Content-Range"), "bytes %d-%d/%d", &start, &end, &total); err != nil ||
			start != offset || end != offset+length-1 || total != reader.size ||
			(response.ContentLength >= 0 && response.ContentLength != length) {
			return nil, errISORangeUnavailable
		}
		if _, err := io.ReadFull(response.Body, data); err != nil {
			return nil, errors.New("镜像分段读取未完成")
		}
	}
	reader.downloaded += length
	entry := reader.order.PushFront(isoRangeCacheBlock{offset: offset, data: data})
	reader.blocks[offset] = entry
	if reader.order.Len() > isoRangeCacheBytes/int(reader.blockSize()) {
		old := reader.order.Back()
		delete(reader.blocks, old.Value.(isoRangeCacheBlock).offset)
		reader.order.Remove(old)
	}
	return data, nil
}

func (reader *isoRangeReader) ReadAt(data []byte, offset int64) (int, error) {
	if err := reader.control.wait(reader.ctx); err != nil {
		return 0, err
	}
	reader.mu.Lock()
	defer reader.mu.Unlock()
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	if offset < 0 {
		return 0, errors.New("镜像读取位置无效")
	}
	if len(data) == 0 {
		return 0, nil
	}
	if offset >= reader.size {
		return 0, io.EOF
	}
	read := 0
	for read < len(data) && offset < reader.size {
		if err := reader.control.wait(reader.ctx); err != nil {
			return read, err
		}
		blockSize := reader.blockSize()
		base := offset / blockSize * blockSize
		block, err := reader.block(base)
		if err != nil {
			return read, err
		}
		n := copy(data[read:], block[offset-base:])
		read += n
		offset += int64(n)
	}
	if read < len(data) {
		return read, io.EOF
	}
	return read, nil
}

type isoStreamFile struct {
	name   string
	size   int64
	reader io.ReaderAt
}

// Each UDF reader retains all of its allocation extents, including fragmented files.
func isoUDFFiles(reader io.ReaderAt) (map[string]isoStreamFile, error) {
	volume, err := udf.NewUdfFromReader(reader)
	if err != nil {
		return nil, err
	}
	root, err := volume.ReadDir(nil)
	if err != nil {
		return nil, err
	}
	files := map[string]isoStreamFile{}
	entries := 0
	var walk func([]udf.File, string, int) error
	walk = func(children []udf.File, parent string, depth int) error {
		if depth > 12 {
			return errors.New("光盘目录层级过深")
		}
		for i := range children {
			entries++
			if entries > 8192 {
				return errors.New("光盘文件数量过多")
			}
			file := &children[i]
			name := file.Name()
			if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\\r\n\x00") {
				continue
			}
			name = path.Join(parent, name)
			entry, err := file.FileEntry()
			if err != nil {
				return err
			}
			if file.IsDir() {
				if entry.InformationLength > 1<<20 {
					return errors.New("光盘目录过大")
				}
				children, err := file.ReadDir()
				if err != nil {
					return err
				}
				if err := walk(children, name, depth+1); err != nil {
					return err
				}
			} else {
				if file.Size() <= 0 {
					continue
				}
				data, err := file.NewReader()
				if err != nil {
					return err
				}
				key := strings.ToUpper(name)
				if _, duplicate := files[key]; duplicate {
					return errors.New("光盘文件名称不唯一")
				}
				files[key] = isoStreamFile{name: name, size: file.Size(), reader: data}
			}
		}
		return nil
	}
	if err := walk(root, "", 0); err != nil {
		return nil, err
	}
	return files, nil
}

type isoJoinedReader struct {
	files []isoStreamFile
	size  int64
}

func (reader *isoJoinedReader) ReadAt(data []byte, offset int64) (int, error) {
	if offset < 0 {
		return 0, errors.New("视频读取位置无效")
	}
	if len(data) == 0 {
		return 0, nil
	}
	read := 0
	for _, file := range reader.files {
		if offset >= file.size {
			offset -= file.size
			continue
		}
		n := min(int64(len(data)-read), file.size-offset)
		got, err := file.reader.ReadAt(data[read:read+int(n)], offset)
		read += got
		if got != int(n) || (err != nil && err != io.EOF) {
			return read, io.ErrUnexpectedEOF
		}
		offset = 0
		if read == len(data) {
			return read, nil
		}
	}
	return read, io.EOF
}

type isoOnlineInput struct {
	URL   string
	Args  []string
	Mode  string
	close func()
}

type isoLocalReader struct {
	file   *os.File
	cached *isoRangeReader
}

func (reader *isoLocalReader) Size() int64 { return reader.cached.Size() }

func (reader *isoLocalReader) ReadAt(data []byte, offset int64) (int, error) {
	return reader.cached.ReadAt(data, offset)
}

type isoPlaylistClip struct {
	file    isoStreamFile
	in, out float64
}

// Linear Blu-ray playlists use 45 kHz timestamps. Complex angles/subpaths retain the libbluray fallback.
func isoBluRayPlaylist(data []byte, directory string, files map[string]isoStreamFile) ([]isoPlaylistClip, float64, error) {
	invalid := errors.New("蓝光播放列表暂不支持在线读取")
	if len(data) < 20 || string(data[:4]) != "MPLS" {
		return nil, 0, invalid
	}
	if version := string(data[4:8]); version != "0100" && version != "0200" && version != "0300" {
		return nil, 0, invalid
	}
	start := int(binary.BigEndian.Uint32(data[8:12]))
	if start < 20 || start > len(data)-10 {
		return nil, 0, invalid
	}
	length := int(binary.BigEndian.Uint32(data[start : start+4]))
	if length < 6 || length > len(data)-start-4 {
		return nil, 0, invalid
	}
	end := start + 4 + length
	count := int(binary.BigEndian.Uint16(data[start+6 : start+8]))
	if count < 1 || count > 1024 {
		return nil, 0, invalid
	}
	unsupported := binary.BigEndian.Uint16(data[start+8:start+10]) != 0
	position := start + 10
	var clips []isoPlaylistClip
	var duration float64
	for i := 0; i < count; i++ {
		if position > end-2 {
			return nil, 0, invalid
		}
		length := int(binary.BigEndian.Uint16(data[position : position+2]))
		if length < 32 || length > end-position-2 {
			return nil, 0, invalid
		}
		item := data[position+2 : position+2+length]
		if string(item[5:9]) != "M2TS" || strings.Trim(string(item[:5]), "0123456789") != "" {
			return nil, 0, invalid
		}
		file, ok := files[strings.ToUpper(path.Join(directory, "STREAM", string(item[:5])+".m2ts"))]
		in := float64(binary.BigEndian.Uint32(item[12:16])) / 45000
		out := float64(binary.BigEndian.Uint32(item[16:20])) / 45000
		if out <= in {
			return nil, 0, invalid
		}
		// Rank unsupported titles too, so a short trailer cannot replace a complex main movie.
		unsupported = unsupported || !ok || item[10]&0x10 != 0 || item[29] != 0
		clips = append(clips, isoPlaylistClip{file: file, in: in, out: out})
		duration += out - in
		position += 2 + length
	}
	if unsupported {
		return nil, duration, invalid
	}
	return clips, duration, nil
}

func isoBluRayMain(files map[string]isoStreamFile) ([]isoPlaylistClip, error) {
	names := make([]string, 0)
	for name, file := range files {
		if strings.HasSuffix(name, ".MPLS") && strings.HasSuffix(path.Dir(name), "BDMV/PLAYLIST") && file.size <= 1<<20 {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	var selected []isoPlaylistClip
	var longest float64
	var selectedErr error
	for _, name := range names {
		file := files[name]
		data := make([]byte, file.size)
		if _, err := file.reader.ReadAt(data, 0); err != nil {
			return nil, err
		}
		clips, duration, err := isoBluRayPlaylist(data, path.Dir(path.Dir(file.name)), files)
		// Discs also contain menu/still/PiP playlists. Only the main title must be readable.
		if duration > longest || (duration == longest && selectedErr != nil && err == nil) {
			selected, longest, selectedErr = clips, duration, err
		}
	}
	if selectedErr != nil {
		return nil, selectedErr
	}
	if len(selected) == 0 {
		return nil, errors.New("蓝光镜像中没有可在线读取的正片播放列表")
	}
	return selected, nil
}

func isoBluRayNavigation(files map[string]isoStreamFile) bool {
	for name := range files {
		if strings.HasSuffix(name, "BDMV/INDEX.BDMV") ||
			(strings.Contains(name, "BDMV/CLIPINF/") && strings.HasSuffix(name, ".CLPI")) {
			return true
		}
	}
	return false
}

// The virtual movie is reachable only through a random path on a temporary loopback listener.
// It never passes the source URL or this listener URL to a public playback response.
func serveISOOnline(ctx context.Context, movie *isoJoinedReader, clips []isoPlaylistClip) (*isoOnlineInput, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	endpoint := "/" + id()
	base := "http://" + listener.Addr().String() + endpoint
	resources := map[string]isoStreamFile{}
	var playlist strings.Builder
	if len(clips) > 0 {
		playlist.WriteString("ffconcat version 1.0\n")
		for i, clip := range clips {
			name := "/clip-" + strconv.Itoa(i) + ".m2ts"
			resources[endpoint+name] = clip.file
			fmt.Fprintf(&playlist, "file '%s'\ninpoint %.6f\noutpoint %.6f\nduration %.6f\n", base+name, clip.in, clip.out, clip.out-clip.in)
		}
	}
	server := &http.Server{ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second,
		BaseContext: func(net.Listener) context.Context { return ctx },
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				http.NotFound(w, r)
				return
			}
			if len(clips) > 0 {
				if r.URL.Path == endpoint+"/playlist.ffconcat" {
					http.ServeContent(w, r, "playlist.ffconcat", time.Time{}, strings.NewReader(playlist.String()))
					return
				}
				file, ok := resources[r.URL.Path]
				if !ok {
					http.NotFound(w, r)
					return
				}
				w.Header().Set("Content-Type", "video/mp2t")
				http.ServeContent(w, r, "movie.m2ts", time.Time{}, io.NewSectionReader(file.reader, 0, file.size))
				return
			}
			if r.URL.Path != endpoint+"/movie" {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/octet-stream")
			http.ServeContent(w, r, "movie", time.Time{}, io.NewSectionReader(movie, 0, movie.size))
		})}
	done := make(chan struct{})
	var once sync.Once
	cleanup := func() { once.Do(func() { close(done); _ = server.Close() }) }
	go func() { _ = server.Serve(listener); cleanup() }()
	go func() {
		select {
		case <-ctx.Done():
			cleanup()
		case <-done:
		}
	}()
	if len(clips) > 0 {
		return &isoOnlineInput{URL: base + "/playlist.ffconcat", Args: []string{"-f", "concat", "-safe", "0"}, close: cleanup}, nil
	}
	return &isoOnlineInput{URL: base + "/movie", close: cleanup}, nil
}

func (a *App) prepareISOOnline(ctx context.Context, input string, info isoMediaInfo, job *featureTranscode) (*isoOnlineInput, error) {
	if info.Size <= 0 {
		return nil, errISORangeUnavailable
	}
	a.features.cacheMu.Lock()
	job.State = "reading-disc"
	a.features.cacheMu.Unlock()
	metadataCtx, cancel := playbackTaskTimeout(ctx, job.control, 30*time.Second)
	defer cancel()
	var source io.ReaderAt
	var remote *isoRangeReader
	var local *isoLocalReader
	if strings.HasPrefix(input, "http") {
		remote = newISORangeReader(metadataCtx, input, info.Size)
		remote.control = job.control
		source = remote
	} else {
		file, err := os.Open(input)
		if err != nil {
			return nil, err
		}
		cached := newISORangeReader(metadataCtx, "", info.Size)
		cached.local, cached.control = file, job.control
		local = &isoLocalReader{file: file, cached: cached}
		source = local
	}
	keepLocal := false
	defer func() {
		if local != nil && !keepLocal {
			_ = local.file.Close()
		}
	}()
	files, err := isoUDFFiles(source)
	listing := make([]isoArchiveFile, 0, len(files))
	for _, file := range files {
		listing = append(listing, isoArchiveFile{Path: file.name, Size: file.size})
	}
	selected, bluray := isoMainFiles(listing)
	var clips []isoPlaylistClip
	if err == nil && bluray {
		clips, err = isoBluRayMain(files)
	}
	if err == nil && len(selected) == 0 && len(clips) == 0 {
		err = errors.New("光盘中没有找到正片视频")
	}
	var online *isoOnlineInput
	native := false
	if err != nil || (bluray && isoBluRayNavigation(files)) {
		if errors.Is(err, errISORangeUnavailable) {
			return nil, err
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// A mature reader handles metadata partitions and complex primary playlists before
		// considering a complete-image download. CLPI navigation also avoids timestamp gaps
		// when an authored linear playlist has preroll or different stream start times.
		nativeCtx, nativeCancel := playbackTaskTimeout(ctx, job.control, 60*time.Second)
		defer nativeCancel()
		if remote != nil {
			remote.mu.Lock()
			remote.ctx, remote.downloaded = nativeCtx, 0
			remote.mu.Unlock()
		} else {
			local.cached.mu.Lock()
			local.cached.ctx, local.cached.downloaded = nativeCtx, 0
			local.cached.mu.Unlock()
		}
		online, err = prepareISOBluRay(ctx, nativeCtx, source, info.Size, job.control)
		if err != nil {
			return nil, err
		}
		native = true
		selected = nil
	}
	movie := &isoJoinedReader{}
	for _, entry := range selected {
		file := files[strings.ToUpper(entry.Path)]
		if file.size > info.Size-movie.size {
			return nil, errors.New("光盘正片大小无效")
		}
		movie.files = append(movie.files, file)
		movie.size += file.size
	}
	// Directory parsing is bounded; subsequent movie reads may stream for the whole playback.
	if remote != nil {
		remote.startStreaming(ctx)
	} else {
		local.cached.startStreaming(ctx)
	}
	if online == nil {
		online, err = serveISOOnline(ctx, movie, clips)
		if err != nil {
			return nil, err
		}
	}
	if local != nil {
		online.Mode = "local"
		keepLocal = true
		closeServer := online.close
		closed := make(chan struct{})
		var once sync.Once
		online.close = func() {
			once.Do(func() {
				closeServer()
				_ = local.file.Close()
				close(closed)
			})
		}
		go func() {
			select {
			case <-ctx.Done():
				online.close()
			case <-closed:
			}
		}()
	} else {
		online.Mode = "range"
	}
	if native {
		online.Mode += "-bluray"
	}
	return online, nil
}
