package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func streamTestBox(kind string, payload []byte) []byte {
	box := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(box[:4], uint32(len(box)))
	copy(box[4:8], kind)
	copy(box[8:], payload)
	return box
}

func TestPlaybackStreamReadinessRejectsEmptyAndPartialMovies(t *testing.T) {
	initial := append(streamTestBox("ftyp", []byte("isom")), streamTestBox("moov", make([]byte, 128))...)
	fragment := append(initial, streamTestBox("moof", make([]byte, 32))...)
	media := streamTestBox("mdat", []byte{1, 2, 3, 4})
	for _, tc := range []struct {
		name  string
		data  []byte
		ready bool
	}{
		{"headers", initial, false}, {"fragment-only", fragment, false},
		{"empty-payload", append(append([]byte(nil), fragment...), media[:8]...), false},
		{"first-media-byte", append(append([]byte(nil), fragment...), media[:9]...), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var detector mp4MediaDetector
			for _, b := range tc.data {
				detector.feed([]byte{b})
			}
			if detector.media != tc.ready {
				t.Fatalf("stream ready=%v", detector.media)
			}
			path := filepath.Join(t.TempDir(), "video.mp4")
			if err := os.WriteFile(path, tc.data, 0600); err != nil {
				t.Fatal(err)
			}
			if playbackFileReady(path) != tc.ready {
				t.Fatal("file readiness differs")
			}
		})
	}
	var extended [16]byte
	binary.BigEndian.PutUint32(extended[:4], 1)
	copy(extended[4:8], "mdat")
	binary.BigEndian.PutUint64(extended[8:], 17)
	var detector mp4MediaDetector
	detector.feed(fragment)
	for _, b := range append(extended[:], 7) {
		detector.feed([]byte{b})
	}
	if !detector.media {
		t.Fatal("64-bit box size was not recognized")
	}
}

func TestPlaybackStreamBackpressureAndCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := newPlaybackStream(ctx)
	done := make(chan error, 1)
	go func() { _, err := s.Write(make([]byte, 2<<20)); done <- err }()
	deadline := time.Now().Add(time.Second)
	for len(s.chunks) < cap(s.chunks) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(s.chunks) != 16 {
		t.Fatal("buffer did not fill")
	}
	select {
	case <-done:
		t.Fatal("producer continued without a reader")
	default:
	}
	if chunk := <-s.chunks; len(chunk) > 32*1024 {
		t.Fatal("chunk exceeded limit")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("blocked writer leaked")
	}
}

func TestPlaybackStreamNativeSeekAndNoOutputCache(t *testing.T) {
	root := os.Getenv("ISO_NATIVE_FIXTURE_ROOT")
	if root == "" {
		t.Skip("ISO_NATIVE_FIXTURE_ROOT required")
	}
	for _, start := range []int{0, 5, 12} {
		t.Run(strconv.Itoa(start), func(t *testing.T) {
			a, _, _, _ := cloudProtectionFixture(t)
			a.features.ctx = context.Background()
			a.features.transcodes = map[string]*featureTranscode{}
			t.Cleanup(func() { a.stopFeatures(); a.features.wg.Wait() })
			if err := a.saveFeatureSetting("playback", featurePlaybackConfig{Transcode: true, Threads: 2, Concurrency: 2, CacheGB: 1, Bitrate: 1000, RetentionDays: 1}); err != nil {
				t.Fatal(err)
			}
			x, err := a.item("movie")
			if err != nil {
				t.Fatal(err)
			}
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Range") == "" {
					t.Error("whole ISO download attempted")
					http.Error(w, "Range required", 500)
					return
				}
				http.ServeFile(w, r, filepath.Join(root, "native-angles.iso"))
			}))
			defer upstream.Close()
			if _, err := a.db.Exec("UPDATE items SET url=? WHERE id='movie'", upstream.URL+"/native-angles.iso"); err != nil {
				t.Fatal(err)
			}
			x.URL = upstream.URL + "/native-angles.iso"
			if err := a.saveMedia(x, M{"RunTimeTicks": int64(12 * 1e7)}); err != nil {
				t.Fatal(err)
			}
			response := httptest.NewRecorder()
			isoFeatureRequest(a, response, httptest.NewRequest("POST", "/features/playback?api_key=viewer-token", strings.NewReader(`{"ID":"movie","Audio":-1,"Stream":true,"Start":`+strconv.Itoa(start)+`}`)))
			if response.Code != 200 {
				t.Fatalf("create %d %s", response.Code, response.Body)
			}
			var info struct{ ID, URL string }
			if err := json.Unmarshal(response.Body.Bytes(), &info); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			movie := httptest.NewRecorder()
			r := httptest.NewRequest("GET", info.URL+"?api_key=viewer-token", nil).WithContext(ctx)
			r.Header.Set("Range", "bytes=0-")
			isoFeatureRequest(a, movie, r)
			if movie.Code != 200 || ctx.Err() != nil {
				t.Fatalf("stream failed %d %v %s", movie.Code, ctx.Err(), movie.Body.Bytes()[:min(100, movie.Body.Len())])
			}
			a.features.cacheMu.Lock()
			job := a.features.transcodes[info.ID]
			a.features.cacheMu.Unlock()
			<-job.finished
			if job.Error != "" || !job.stream.ready.Load() {
				t.Fatalf("job failed: %s", job.Error)
			}
			if start == 12 && job.Start != 0 {
				t.Fatal("EOF resume was not reset")
			}
			if job.SourceMode != "range-bluray" {
				t.Fatal(job.SourceMode)
			}
			for _, name := range []string{"video.mp4", "source.iso"} {
				if _, err := os.Stat(filepath.Join(job.Directory, name)); !os.IsNotExist(err) {
					t.Fatalf("cached %s: %v", name, err)
				}
			}
			path := filepath.Join(t.TempDir(), "received.mp4")
			if err := os.WriteFile(path, movie.Body.Bytes(), 0600); err != nil {
				t.Fatal(err)
			}
			probe, err := transferProbe(context.Background(), path)
			if err != nil || len(probe.Streams) < 2 || probe.Streams[0].CodecName != "h264" || probe.Streams[1].CodecName != "aac" {
				t.Fatalf("invalid stream %+v %v", probe, err)
			}
			expected := 12 - start
			if start == 12 {
				expected = 12
			}
			duration, _ := strconv.ParseFloat(probe.Format.Duration, 64)
			if duration < float64(expected)-1 || duration > float64(expected)+1 {
				t.Fatalf("seek duration=%g want %d", duration, expected)
			}
			if !bytes.Contains(movie.Body.Bytes(), []byte("moof")) {
				t.Fatal("missing fragments")
			}
		})
	}
}

func TestPlaybackStreamOwnershipSingleReaderAndDisconnect(t *testing.T) {
	a, _, _, _ := cloudProtectionFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	key := strings.Repeat("a", 40)
	job := &featureTranscode{ID: key, Item: "movie", Owner: "viewer", Stream: true, stream: newPlaybackStream(ctx), cancel: cancel}
	a.features.transcodes = map[string]*featureTranscode{key: job}
	path := "/features/stream/" + key + "/video.mp4?api_key=viewer-token"
	denied := httptest.NewRecorder()
	a.featureStream(denied, httptest.NewRequest("GET", path, nil), User{ID: "other"})
	if denied.Code != 404 || job.stream.claimed.Load() {
		t.Fatal("another user claimed output")
	}
	badRange := httptest.NewRecorder()
	rangeRequest := httptest.NewRequest("GET", path, nil)
	rangeRequest.Header.Set("Range", "bytes=1024-")
	isoFeatureRequest(a, badRange, rangeRequest)
	if badRange.Code != 416 || job.stream.claimed.Load() {
		t.Fatal("invalid range claimed output")
	}
	head := httptest.NewRecorder()
	isoFeatureRequest(a, head, httptest.NewRequest("HEAD", path, nil))
	if head.Code != 200 || job.stream.claimed.Load() {
		t.Fatal("HEAD consumed output")
	}
	done := make(chan error, 1)
	go func() {
		defer close(job.stream.chunks)
		chunk := make([]byte, 32*1024)
		for i := 0; i < 2048; i++ {
			if _, err := job.stream.Write(chunk); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { isoFeatureRequest(a, w, r) }))
	defer server.Close()
	response, err := http.Get(server.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatal(response.StatusCode)
	}
	if _, err := io.CopyN(io.Discard, response.Body, 32*1024); err != nil {
		t.Fatal(err)
	}
	duplicate, err := http.Get(server.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	duplicate.Body.Close()
	if duplicate.StatusCode != 409 {
		t.Fatal("duplicate reader accepted")
	}
	response.Body.Close()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("producer continued: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("disconnected producer leaked")
	}
}
