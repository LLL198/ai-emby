package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestPlaybackTaskPausePreservesProbeBudget(t *testing.T) {
	control := newPlaybackTaskControl()
	if err := control.change("pause"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := playbackTaskTimeout(context.Background(), control, 250*time.Millisecond)
	defer cancel()
	finished := make(chan error, 1)
	go func() { finished <- control.wait(ctx) }()
	select {
	case err := <-finished:
		t.Fatalf("paused work did not wait: %v", err)
	case <-time.After(600 * time.Millisecond):
	}
	if ctx.Err() != nil {
		t.Fatal("pause consumed the probe timeout")
	}
	if err := control.change("resume"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("resume did not release reads")
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("active probe budget did not expire")
	}
}

func TestPlaybackTaskSuspendsAndCancelsFFmpeg(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("FFmpeg required")
	}
	control := newPlaybackTaskControl()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	path := filepath.Join(t.TempDir(), "video.mp4")
	cmd := exec.CommandContext(ctx, "ffmpeg", "-v", "error", "-nostdin", "-re", "-f", "lavfi", "-i", "testsrc2=size=320x180:rate=24", "-t", "20", "-c:v", "libx264", "-threads", "1", "-preset", "ultrafast", "-g", "6", "-movflags", "+frag_keyframe+empty_moov", "-f", "mp4", path)
	finished := make(chan error, 1)
	go func() { finished <- control.run(ctx, cmd) }()
	size := func() int64 {
		info, _ := os.Stat(path)
		if info == nil {
			return 0
		}
		return info.Size()
	}
	waitGrowth := func(minimum int64) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for size() <= minimum && time.Now().Before(deadline) {
			time.Sleep(50 * time.Millisecond)
		}
		if size() <= minimum {
			t.Fatal("FFmpeg output did not grow")
		}
	}
	waitGrowth(4096)
	if err := control.change("pause"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	pausedSize := size()
	time.Sleep(400 * time.Millisecond)
	if size() != pausedSize {
		t.Fatal("FFmpeg kept producing video while paused")
	}
	if err := control.change("resume"); err != nil {
		t.Fatal(err)
	}
	waitGrowth(pausedSize)
	if err := control.change("pause"); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("cancel did not terminate a suspended FFmpeg process")
	}
}

func TestPlaybackTaskPausesFullImageDownload(t *testing.T) {
	data := bytes.Repeat([]byte("movie-image"), 8192)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for offset := 0; offset < len(data); offset += 4096 {
			end := min(offset+4096, len(data))
			if _, err := w.Write(data[offset:end]); err != nil {
				return
			}
			w.(http.Flusher).Flush()
			time.Sleep(10 * time.Millisecond)
		}
	}))
	defer server.Close()
	for _, stop := range []bool{false, true} {
		control := newPlaybackTaskControl()
		ctx, cancel := context.WithCancel(context.Background())
		path := filepath.Join(t.TempDir(), "source.iso")
		var progress atomic.Int64
		paused := make(chan struct{})
		finished := make(chan error, 1)
		go func() {
			finished <- downloadISOControlled(ctx, server.URL, path, int64(len(data)), int64(len(data)+1), func(n, total int64) {
				if progress.Swap(n) == 0 {
					_ = control.change("pause")
					close(paused)
				}
			}, control)
		}()
		select {
		case <-paused:
		case <-time.After(3 * time.Second):
			cancel()
			t.Fatal("download did not begin")
		}
		count := progress.Load()
		time.Sleep(250 * time.Millisecond)
		if progress.Load() != count {
			cancel()
			t.Fatal("paused download kept reading")
		}
		if stop {
			cancel()
		} else {
			_ = control.change("resume")
		}
		select {
		case err := <-finished:
			if !stop && err != nil {
				t.Fatal(err)
			}
			if stop && err == nil {
				t.Fatal("canceled download succeeded")
			}
		case <-time.After(3 * time.Second):
			cancel()
			t.Fatal("download did not terminate")
		}
		cancel()
		if stop {
			if _, err := os.Stat(path + ".partial"); !os.IsNotExist(err) {
				t.Fatal("canceled download left a partial file")
			}
		} else if output, err := os.ReadFile(path); err != nil || !bytes.Equal(output, data) {
			t.Fatal("resume corrupted the image")
		}
	}
}

func TestPlaybackTaskControlsOwnershipAndSafeDeletion(t *testing.T) {
	a, _, _, _ := cloudProtectionFixture(t)
	a.features.transcodes = map[string]*featureTranscode{}
	t.Cleanup(func() { a.stopFeatures(); a.features.wg.Wait() })
	key := strings.Repeat("a", 40)
	dir := filepath.Join(featureDataRoot(), "playback", key)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(dir, "video.mp4"), []byte("partial"), 0600)
	ctx, cancel := context.WithCancel(context.Background())
	job := &featureTranscode{ID: key, Item: "movie", Owner: "viewer", Directory: dir, State: "transcoding", Used: time.Now(), control: newPlaybackTaskControl(), cancel: cancel, finished: make(chan struct{})}
	a.features.transcodes[key] = job
	command := func(action string, user User) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/features/playback-control", strings.NewReader(featureJSON(M{"ID": key, "Action": action})))
		a.featurePlaybackControl(w, r, user)
		return w
	}
	if w := command("pause", User{ID: "other"}); w.Code != 404 {
		t.Fatalf("other account controlled a task: %d", w.Code)
	}
	if w := command("pause", User{ID: "viewer", API: true}); w.Code != 403 {
		t.Fatal("API key controlled a task")
	}
	if w := command("invalid", User{ID: "viewer"}); w.Code != 400 {
		t.Fatal("invalid action accepted")
	}
	if w := command("pause", User{ID: "viewer"}); w.Code != 200 || !job.Paused {
		t.Fatal("owner could not pause")
	}
	if w := command("resume", User{ID: "administrator", Admin: true}); w.Code != 200 || job.Paused {
		t.Fatal("admin could not resume")
	}
	if w := command("delete", User{ID: "viewer"}); w.Code != 200 {
		t.Fatalf("delete: %d %s", w.Code, w.Body)
	}
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatal("deletion did not stop the worker")
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatal("files removed before the worker exited")
	}
	// Simulate the final pending worker write. Deletion must wait for it.
	_ = os.WriteFile(filepath.Join(dir, "record.json"), []byte("final write"), 0600)
	close(job.finished)
	a.features.wg.Wait()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("deleted task files survived or were recreated")
	}
	if a.features.transcodes[key] != nil {
		t.Fatal("deleted task still registered")
	}
	outside := filepath.Join(t.TempDir(), "sentinel")
	_ = os.WriteFile(outside, []byte("keep"), 0600)
	_ = os.MkdirAll(dir, 0700)
	record := featureTranscode{ID: key, Item: "movie", Owner: "viewer", Directory: filepath.Dir(outside), State: "stopped", Done: true}
	data, _ := json.Marshal(record)
	_ = os.WriteFile(filepath.Join(dir, "record.json"), data, 0600)
	if w := command("delete", User{ID: "viewer"}); w.Code != 200 {
		t.Fatal("persisted stopped task not deletable")
	}
	a.features.wg.Wait()
	if _, err := os.Stat(outside); err != nil {
		t.Fatal("deletion trusted a persisted external directory")
	}
}

func TestPlaybackTaskExpiresDisconnectedBrowser(t *testing.T) {
	t.Setenv("MEDIA_INFO_ROOT", t.TempDir())
	for _, complete := range []bool{false, true} {
		a := &App{}
		key := strings.Repeat("b", 40)
		dir := filepath.Join(featureDataRoot(), "playback", key)
		_ = os.MkdirAll(dir, 0700)
		_ = os.WriteFile(filepath.Join(dir, "video.mp4"), []byte("cached output"), 0600)
		ctx, cancel := context.WithCancel(context.Background())
		control := newPlaybackTaskControl()
		_ = control.change("pause")
		job := &featureTranscode{ID: key, Used: time.Now(), heartbeat: time.Now().Add(-6 * time.Minute), control: control, cancel: cancel, Paused: true, Done: complete, finished: make(chan struct{})}
		a.features.transcodes = map[string]*featureTranscode{key: job, "active": {Used: time.Now(), cancel: func() { t.Error("active player was stopped") }}}
		a.expirePlaybackTasks()
		if !job.deleting || job.State != "deleting" || job.Paused || (!complete && ctx.Err() == nil) {
			close(job.finished)
			cancel()
			a.features.wg.Wait()
			t.Fatal("abandoned task was not scheduled for deletion")
		}
		close(job.finished)
		cancel()
		a.features.wg.Wait()
		if a.features.transcodes[key] != nil {
			t.Fatal("expired task still registered")
		}
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatal("expired task left cached files")
		}
	}
}

func TestPlaybackTaskQuickReopenWaitsForPreviousDeletion(t *testing.T) {
	root := os.Getenv("ISO_FIXTURE_ROOT")
	if root == "" {
		t.Skip("ISO_FIXTURE_ROOT required")
	}
	a, _, _, _ := cloudProtectionFixture(t)
	a.features.ctx = context.Background()
	a.features.transcodes = map[string]*featureTranscode{}
	t.Cleanup(func() { a.stopFeatures(); a.features.wg.Wait() })
	if err := a.saveFeatureSetting("playback", featurePlaybackConfig{Transcode: true, Threads: 2, Concurrency: 2, CacheGB: 1, Bitrate: 1000, RetentionDays: 1}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.FileServer(http.Dir(root)))
	defer server.Close()
	_, err := a.db.Exec("UPDATE items SET url=? WHERE id='movie'", server.URL+"/dvd.iso")
	if err != nil {
		t.Fatal(err)
	}
	play := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		isoFeatureRequest(a, w, httptest.NewRequest("POST", "/features/playback?api_key=viewer-token", strings.NewReader(`{"ID":"movie","Audio":-1,"Start":0}`)))
		return w
	}
	first := play()
	if first.Code != 200 {
		t.Fatalf("start: %d %s", first.Code, first.Body)
	}
	var response struct {
		ID   string
		Done bool
	}
	_ = json.Unmarshal(first.Body.Bytes(), &response)
	a.features.cacheMu.Lock()
	old := a.features.transcodes[response.ID]
	a.features.cacheMu.Unlock()
	select {
	case <-old.finished:
	case <-time.After(10 * time.Second):
		t.Fatal("first playback did not finish")
	}
	a.features.wg.Wait()
	// Hold cleanup open to reproduce reopening while the old reader exits.
	a.features.cacheMu.Lock()
	old.finished = make(chan struct{})
	a.features.cacheMu.Unlock()
	t.Cleanup(func() {
		select {
		case <-old.finished:
		default:
			close(old.finished)
		}
	})
	deleted := httptest.NewRecorder()
	isoFeatureRequest(a, deleted, httptest.NewRequest("POST", "/features/playback-control?api_key=viewer-token", strings.NewReader(featureJSON(M{"ID": response.ID, "Action": "delete"}))))
	if deleted.Code != 200 {
		t.Fatalf("delete: %d %s", deleted.Code, deleted.Body)
	}
	reopened := make(chan *httptest.ResponseRecorder, 1)
	go func() { reopened <- play() }()
	select {
	case w := <-reopened:
		t.Fatalf("reopen raced cleanup: %d %s", w.Code, w.Body)
	case <-time.After(200 * time.Millisecond):
	}
	close(old.finished)
	select {
	case w := <-reopened:
		if w.Code != 200 {
			t.Fatalf("reopen: %d %s", w.Code, w.Body)
		}
		_ = json.Unmarshal(w.Body.Bytes(), &response)
		if response.Done {
			t.Fatal("reopen reused old completed output")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reopen did not resume after deletion")
	}
	a.features.cacheMu.Lock()
	newJob := a.features.transcodes[response.ID]
	a.features.cacheMu.Unlock()
	if newJob == nil || newJob == old {
		t.Fatal("reopen did not create a new task")
	}
	separate := httptest.NewRecorder()
	isoFeatureRequest(a, separate, httptest.NewRequest("POST", "/features/playback?api_key=viewer-token", strings.NewReader(`{"ID":"movie","Audio":-1,"Start":0,"Session":"11111111111111111111111111111111"}`)))
	if separate.Code != 200 {
		t.Fatalf("separate session: %d %s", separate.Code, separate.Body)
	}
	var second struct{ ID string }
	_ = json.Unmarshal(separate.Body.Bytes(), &second)
	if second.ID == newJob.ID {
		t.Fatal("independent playback sessions shared a task")
	}
	cleanup := httptest.NewRecorder()
	isoFeatureRequest(a, cleanup, httptest.NewRequest("POST", "/features/playback-control?api_key=viewer-token", strings.NewReader(featureJSON(M{"ID": newJob.ID, "Action": "delete"}))))
	if cleanup.Code != 200 {
		t.Fatal("previous session could not be deleted")
	}
	a.features.cacheMu.Lock()
	secondJob := a.features.transcodes[second.ID]
	if secondJob == nil || secondJob.deleting {
		t.Error("previous exit deleted another session")
	}
	a.features.cacheMu.Unlock()
}
