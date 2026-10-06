package main

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"os/exec"
	"sync"
)

const isoNativeReadSize = 1 << 20

var errISODiscReaderUnavailable = errors.New("蓝光按需读取组件尚未安装，请更新 Docker 镜像")
var errISODiscRead = errors.New("蓝光正片按需读取失败，镜像可能损坏或包含不支持的光盘结构")

// The helper only receives a random loopback URL. Cloud credentials and public source URLs
// stay in the worker, which continues enforcing Range validation and bounded memory caching.
type isoNativeReader struct {
	ctx      context.Context
	control  *playbackTaskControl
	input    io.WriteCloser
	output   io.ReadCloser
	command  *exec.Cmd
	cancel   context.CancelFunc
	done     chan struct{}
	mu       sync.Mutex
	once     sync.Once
	size     int64
	duration uint64
	playlist uint32
	timeSeek bool
}

func (reader *isoNativeReader) close() {
	reader.once.Do(func() {
		reader.cancel()
		_ = reader.input.Close()
		_ = reader.output.Close()
		<-reader.done
	})
}

func startISONativeReader(ctx, metadataCtx context.Context, sourceURL string, control *playbackTaskControl) (*isoNativeReader, error) {
	tool, err := exec.LookPath("ai-emby-disc-reader")
	if err != nil {
		return nil, errISODiscReaderUnavailable
	}
	processCtx, cancel := context.WithCancel(ctx)
	command := exec.CommandContext(processCtx, tool, sourceURL)
	input, err := command.StdinPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	output, err := command.StdoutPipe()
	if err != nil {
		cancel()
		_ = input.Close()
		return nil, err
	}
	// libbluray diagnostics can include internal paths; never return them in public errors.
	command.Stderr = io.Discard
	if err := command.Start(); err != nil {
		cancel()
		_ = input.Close()
		_ = output.Close()
		return nil, errISODiscReaderUnavailable
	}
	reader := &isoNativeReader{ctx: ctx, control: control, input: input, output: output, command: command, cancel: cancel, done: make(chan struct{})}
	go func() {
		_ = command.Wait()
		close(reader.done)
	}()
	header := make([]byte, 24)
	ready := make(chan error, 1)
	go func() {
		_, err := io.ReadFull(output, header)
		ready <- err
	}()
	select {
	case err = <-ready:
	case <-metadataCtx.Done():
		reader.close()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, context.DeadlineExceeded
	}
	if err != nil || (string(header[:4]) != "BDR1" && string(header[:4]) != "BDR2") {
		reader.close()
		return nil, errISODiscRead
	}
	size := binary.BigEndian.Uint64(header[4:12])
	reader.duration, reader.playlist = binary.BigEndian.Uint64(header[12:20]), binary.BigEndian.Uint32(header[20:24])
	if size == 0 || size > 1<<63-1 || reader.duration == 0 || reader.playlist > 99999 {
		reader.close()
		return nil, errISODiscRead
	}
	reader.size = int64(size)
	reader.timeSeek = string(header[:4]) == "BDR2"
	return reader, nil
}

// libbluray resolves CLPI access points. The actual time can precede the
// requested time; callers must retain it instead of pretending the seek is exact.
func (reader *isoNativeReader) seekTime(ticks uint64) (int64, uint64, error) {
	if !reader.timeSeek || ticks >= reader.duration {
		return 0, 0, errISODiscRead
	}
	if err := reader.control.wait(reader.ctx); err != nil {
		return 0, 0, err
	}
	reader.mu.Lock()
	defer reader.mu.Unlock()
	var request [16]byte
	copy(request[:], "SEEK")
	binary.BigEndian.PutUint64(request[8:], ticks)
	if _, err := reader.input.Write(request[:]); err != nil {
		return 0, 0, errISODiscRead
	}
	var header [8]byte
	if _, err := io.ReadFull(reader.output, header[:]); err != nil {
		return 0, 0, errISODiscRead
	}
	if binary.BigEndian.Uint32(header[:4]) != 0 || binary.BigEndian.Uint32(header[4:]) != 16 {
		return 0, 0, errISODiscRead
	}
	var point [16]byte
	if _, err := io.ReadFull(reader.output, point[:]); err != nil {
		return 0, 0, errISODiscRead
	}
	offset, actual := binary.BigEndian.Uint64(point[:8]), binary.BigEndian.Uint64(point[8:])
	if offset >= uint64(reader.size) || actual > reader.duration {
		return 0, 0, errISODiscRead
	}
	return int64(offset), actual, nil
}

func (reader *isoNativeReader) ReadAt(data []byte, offset int64) (int, error) {
	if offset < 0 {
		return 0, errors.New("视频读取位置无效")
	}
	if len(data) == 0 {
		return 0, nil
	}
	if err := reader.control.wait(reader.ctx); err != nil {
		return 0, err
	}
	reader.mu.Lock()
	defer reader.mu.Unlock()
	read := 0
	for read < len(data) && offset < reader.size {
		if err := reader.control.wait(reader.ctx); err != nil {
			return read, err
		}
		length := min(int64(len(data)-read), int64(isoNativeReadSize), reader.size-offset)
		request := make([]byte, 16)
		copy(request, "READ")
		binary.BigEndian.PutUint32(request[4:8], uint32(length))
		binary.BigEndian.PutUint64(request[8:16], uint64(offset))
		if _, err := reader.input.Write(request); err != nil {
			return read, errISODiscRead
		}
		header := make([]byte, 8)
		if _, err := io.ReadFull(reader.output, header); err != nil {
			return read, errISODiscRead
		}
		status, got := binary.BigEndian.Uint32(header[:4]), int64(binary.BigEndian.Uint32(header[4:]))
		if status != 0 || got != length {
			return read, errISODiscRead
		}
		if _, err := io.ReadFull(reader.output, data[read:read+int(got)]); err != nil {
			return read, errISODiscRead
		}
		read += int(got)
		offset += got
	}
	if read < len(data) {
		return read, io.EOF
	}
	return read, nil
}

func prepareISOBluRay(ctx, metadataCtx context.Context, source io.ReaderAt, size int64, control *playbackTaskControl) (*isoOnlineInput, error) {
	// This is a private view of the ISO, not a downloaded image.
	image, err := serveISOOnline(ctx, &isoJoinedReader{size: size, files: []isoStreamFile{{size: size, reader: source}}}, nil)
	if err != nil {
		return nil, err
	}
	native, err := startISONativeReader(ctx, metadataCtx, image.URL, control)
	if err != nil {
		image.close()
		return nil, err
	}
	movie, err := serveISOOnline(ctx, &isoJoinedReader{size: native.size, files: []isoStreamFile{{size: native.size, reader: native}}}, nil)
	if err != nil {
		native.close()
		image.close()
		return nil, err
	}
	closeMovie := movie.close
	movie.native = native
	var once sync.Once
	movie.close = func() {
		once.Do(func() {
			closeMovie()
			native.close()
			image.close()
		})
	}
	return movie, nil
}
