package main

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net/http"
	"os"
	"sync/atomic"
)

// Keep at most 512 KiB of output ahead of the browser. Blocking Write applies
// backpressure to ffmpeg instead of building a complete movie on disk.
type playbackStream struct {
	ctx     context.Context
	chunks  chan []byte
	ready   atomic.Bool
	claimed atomic.Bool
	boxes   mp4MediaDetector
}

func newPlaybackStream(ctx context.Context) *playbackStream {
	return &playbackStream{ctx: ctx, chunks: make(chan []byte, 16)}
}

func (s *playbackStream) Write(p []byte) (int, error) {
	written := 0
	for len(p) > 0 {
		n := min(len(p), 32*1024)
		chunk := append([]byte(nil), p[:n]...)
		s.boxes.feed(chunk)
		if s.boxes.media {
			s.ready.Store(true)
		}
		select {
		case s.chunks <- chunk:
			written += n
			p = p[n:]
		case <-s.ctx.Done():
			return written, s.ctx.Err()
		}
	}
	return written, nil
}

// Parse only top-level box headers, retaining no media payload. ftyp/moov alone
// is an empty movie, which ffmpeg can produce when a saved position is at EOF.
type mp4MediaDetector struct {
	header                   [16]byte
	used                     int
	remaining                uint64
	kind                     string
	fragment, media, invalid bool
}

func (d *mp4MediaDetector) feed(p []byte) {
	for len(p) > 0 && !d.invalid && !d.media {
		if d.remaining > 0 {
			n := min(uint64(len(p)), d.remaining)
			if d.kind == "mdat" && d.fragment {
				d.media = true
				return
			}
			p = p[n:]
			d.remaining -= n
			continue
		}
		need := 8
		if d.used >= 8 && binary.BigEndian.Uint32(d.header[:4]) == 1 {
			need = 16
		}
		n := min(len(p), need-d.used)
		copy(d.header[d.used:], p[:n])
		d.used += n
		p = p[n:]
		if d.used < need {
			continue
		}
		size := uint64(binary.BigEndian.Uint32(d.header[:4]))
		if size == 1 && need == 8 {
			continue
		}
		if size == 1 {
			size = binary.BigEndian.Uint64(d.header[8:16])
		}
		if size < uint64(need) {
			d.invalid = true
			return
		}
		d.kind = string(d.header[4:8])
		if d.kind == "moof" {
			d.fragment = true
		}
		d.remaining = size - uint64(need)
		d.used = 0
	}
}

func playbackFileReady(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	var header [16]byte
	fragment := false
	for pos, count := int64(0), 0; pos+8 <= info.Size() && count < 1024; count++ {
		if _, err := f.ReadAt(header[:8], pos); err != nil {
			return false
		}
		size, headerSize := uint64(binary.BigEndian.Uint32(header[:4])), uint64(8)
		if size == 1 {
			if _, err := f.ReadAt(header[8:], pos+8); err != nil {
				return false
			}
			size, headerSize = binary.BigEndian.Uint64(header[8:]), 16
		}
		if size < headerSize || size > 1<<63-1 {
			return false
		}
		kind := string(header[4:8])
		if kind == "moof" {
			fragment = true
		}
		if kind == "mdat" && fragment && size > headerSize && info.Size()-pos > int64(headerSize) {
			return true
		}
		if size > uint64(info.Size()-pos) {
			return false
		}
		pos += int64(size)
	}
	return false
}

func (a *App) servePlaybackPipe(w http.ResponseWriter, r *http.Request, job *featureTranscode) {
	s := job.stream
	if s == nil {
		fail(w, 410, "实时播放已结束，请重新播放")
		return
	}
	if value := r.Header.Get("Range"); value != "" && value != "bytes=0-" {
		fail(w, http.StatusRequestedRangeNotSatisfiable, "请通过播放器时间轴跳转")
		return
	}
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Accept-Ranges", "none")
	w.Header().Set("X-Accel-Buffering", "no")
	if r.Method == http.MethodHead {
		return
	}
	if !s.claimed.CompareAndSwap(false, true) {
		fail(w, 409, "播放流已使用，请重新播放")
		return
	}
	// Abandoning the HTTP response also stops its decoder and private ISO readers.
	defer job.cancel()
	for {
		select {
		case <-r.Context().Done():
			return
		case chunk, ok := <-s.chunks:
			if !ok {
				return
			}
			if _, err := w.Write(chunk); err != nil {
				return
			}
			if flush, ok := w.(http.Flusher); ok {
				flush.Flush()
			}
		}
	}
}

var errPlaybackEmpty = errors.New("没有生成可播放的正片视频，请从头播放或检查媒体源")

var _ io.Writer = (*playbackStream)(nil)
