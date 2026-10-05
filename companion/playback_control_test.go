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
	a := &App{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	control := newPlaybackTaskControl()
	_ = control.change("pause")
	job := &featureTranscode{Used: time.Now(), heartbeat: time.Now().Add(-6 * time.Minute), control: control, cancel: cancel, Paused: true}
	a.features.transcodes = map[string]*featureTranscode{"old": job, "active": {Used: time.Now(), cancel: func() { t.Error("active player was stopped") }}}
	a.expirePlaybackTasks()
	if ctx.Err() == nil || job.State != "stopping" || job.Paused {
		t.Fatal("abandoned paused task kept running")
	}
}
