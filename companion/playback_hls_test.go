package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHLSFragmentValidationAndMemoryLimit(t *testing.T) {
	init := append(streamTestBox("ftyp", []byte("isom")), streamTestBox("moov", make([]byte, 32))...)
	media := append(streamTestBox("moof", make([]byte, 32)), streamTestBox("mdat", []byte{1, 2, 3})...)
	f, err := splitHLSFragment(append(init, media...))
	if err != nil || !bytes.Equal(f.init, init) || !bytes.Equal(f.media, media) {
		t.Fatal("fragment split failed")
	}
	for _, bad := range [][]byte{init, append(init, media[:len(media)-1]...), streamTestBox("mdat", nil)} {
		if _, err := splitHLSFragment(bad); err == nil {
			t.Fatal("empty/truncated fragment accepted")
		}
	}
	var out hlsBoundedOutput
	if _, err := out.Write(make([]byte, hlsSegmentLimit)); err != nil {
		t.Fatal(err)
	}
	if _, err := out.Write([]byte{1}); err == nil || out.Len() != hlsSegmentLimit {
		t.Fatal("output memory cap not enforced")
	}
}

func TestHLSSessionRandomSeekRetryAndDelete(t *testing.T) {
	root := os.Getenv("ISO_NATIVE_FIXTURE_ROOT")
	if root == "" {
		t.Skip("native fixture required")
	}
	a, _, _, _ := cloudProtectionFixture(t)
	a.features.ctx = context.Background()
	a.features.transcodes = map[string]*featureTranscode{}
	defer func() { a.stopFeatures(); a.features.wg.Wait() }()
	if err := a.saveFeatureSetting("playback", featurePlaybackConfig{Transcode: true, Threads: 2, Concurrency: 2, Bitrate: 1000, CacheGB: 1}); err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") == "" {
			t.Error("whole ISO request")
			http.Error(w, "Range required", 500)
			return
		}
		http.ServeFile(w, r, filepath.Join(root, "native-udf250.iso"))
	}))
	defer upstream.Close()
	if _, err := a.db.Exec("UPDATE items SET url=? WHERE id='movie'", upstream.URL+"/movie.iso"); err != nil {
		t.Fatal(err)
	}
	x, _ := a.item("movie")
	a.saveMedia(x, M{"RunTimeTicks": 120000000})
	w := httptest.NewRecorder()
	isoFeatureRequest(a, w, httptest.NewRequest("POST", "/features/playback?api_key=viewer-token", strings.NewReader(`{"ID":"movie","Audio":-1,"HLS":true,"Start":8}`)))
	if w.Code != 200 {
		t.Fatalf("create %d %s", w.Code, w.Body.String())
	}
	var task struct {
		ID, URL string
		HLS     bool
	}
	json.Unmarshal(w.Body.Bytes(), &task)
	if !task.HLS {
		t.Fatal("HLS request ignored")
	}
	a.features.cacheMu.Lock()
	job := a.features.transcodes[task.ID]
	a.features.cacheMu.Unlock()
	deadline := time.Now().Add(40 * time.Second)
	for !job.hls.ready.Load() {
		a.features.cacheMu.Lock()
		done, err := job.Done, job.Error
		a.features.cacheMu.Unlock()
		if done {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatal("session not ready")
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Logf("native index=%v copy=%v points=%d", job.hls.indexed, job.hls.copyVideo, len(job.hls.points))
	base := "/features/hls/" + task.ID + "/"
	for _, endpoint := range []string{"master.m3u8", "index.m3u8", "init-2.mp4", "segment-2.m4s"} {
		denied := httptest.NewRecorder()
		a.featureHLS(denied, httptest.NewRequest("GET", base+endpoint, nil), User{ID: "another-user"})
		if denied.Code != 404 {
			t.Fatalf("another user could access %s: %d", endpoint, denied.Code)
		}
	}
	get := func(path, method, token string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		isoFeatureRequest(a, w, httptest.NewRequest(method, base+path+"?api_key="+token, nil))
		return w
	}
	if w := get("index.m3u8", "GET", "viewer-token"); w.Code != 200 || !strings.Contains(w.Body.String(), "#EXT-X-ENDLIST") || !strings.Contains(w.Body.String(), "segment-2.m4s") {
		t.Fatalf("playlist %d", w.Code)
	}
	if w := get("segment-2.m4s", "HEAD", "viewer-token"); w.Code != 200 {
		t.Fatal("HEAD failed")
	}
	job.hls.mu.Lock()
	count := len(job.hls.cache)
	job.hls.mu.Unlock()
	if count != 0 {
		t.Fatal("HEAD generated media")
	}
	if w := get("segment-2.m4s", "GET", "admin-token"); w.Code == 200 {
		t.Fatal("unknown credential accepted")
	}
	read := func(index string) []byte {
		t.Helper()
		init := get("init-"+index+".mp4", "GET", "viewer-token")
		media := get("segment-"+index+".m4s", "GET", "viewer-token")
		if init.Code != 200 || media.Code != 200 {
			t.Fatalf("fragment %s init=%d media=%d %s", index, init.Code, media.Code, media.Body.String())
		}
		return append(init.Body.Bytes(), media.Body.Bytes()...)
	}
	tail := read("2")
	again := read("2")
	if !bytes.Equal(tail, again) {
		t.Fatal("retry changed cached fragment")
	}
	head := read("0")
	if bytes.Equal(tail, head) {
		t.Fatal("seek did not change media")
	}
	path := filepath.Join(t.TempDir(), "fragment.mp4")
	os.WriteFile(path, tail, 0600)
	cmd := exec.Command("ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "default=nw=1:nk=1", path)
	out, err := cmd.Output()
	if err != nil {
		t.Fatal("fragment not decodable")
	}
	t.Logf("tail duration %s", strings.TrimSpace(string(out)))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _ = job.hls.fragment(ctx, 1)
	if job.hls.ctx.Err() != nil || job.Done {
		t.Fatal("fragment disconnect killed session")
	}
	for _, name := range []string{"source.iso", "video.mp4"} {
		if _, err := os.Stat(filepath.Join(job.Directory, name)); !os.IsNotExist(err) {
			t.Fatal("movie cached to disk")
		}
	}
	w = httptest.NewRecorder()
	isoFeatureRequest(a, w, httptest.NewRequest("POST", "/features/playback-control?api_key=viewer-token", strings.NewReader(`{"ID":"`+task.ID+`","Action":"delete"}`)))
	if w.Code != 200 {
		t.Fatalf("delete %d", w.Code)
	}
	select {
	case <-job.finished:
	case <-time.After(5 * time.Second):
		t.Fatal("session did not stop")
	}
}

func TestHLSSegmentCancellationKeepsSession(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := newHLSPlayback(ctx)
	h.points = []hlsPoint{{time: 0}, {time: 4}, {time: 8}}
	consumer, cancelConsumer := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { _, err := h.fragment(consumer, 0); done <- err }()
	flight := <-h.requests
	cancelConsumer()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("request blocked")
	}
	if flight.ctx.Err() == nil || h.ctx.Err() != nil {
		t.Fatal("cancellation scope incorrect")
	}
	h.finish(flight, nil, context.Canceled)
	go func() {
		f, err := h.fragment(ctx, 0)
		if err == nil {
			_, _ = io.Copy(io.Discard, bytes.NewReader(f.media))
		}
		done <- err
	}()
	retry := <-h.requests
	h.finish(retry, &hlsFragment{init: []byte{1}, media: []byte{2}}, nil)
	if err := <-done; err != nil {
		t.Fatal("retry failed")
	}
}
