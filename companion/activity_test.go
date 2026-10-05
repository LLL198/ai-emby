package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestActivityScanAndUpdate(t *testing.T) {
	a := testApp(t)
	root := t.TempDir()
	for _, n := range []string{"a.strm", "b.strm"} {
		if e := os.WriteFile(filepath.Join(root, n), []byte("https://example.com/video"), 0600); e != nil {
			t.Fatal(e)
		}
	}
	if _, e := a.db.Exec("INSERT INTO libraries(id,name,path,kind) VALUES('l','test',?,'movies')", root); e != nil {
		t.Fatal(e)
	}
	a.scanLibraryMode("l", false)
	a.scanLibraryMode("l", true)
	for _, v := range a.activity.entries {
		if v.State != "complete" || v.Done != 2 || v.Total != 2 {
			t.Fatalf("bad scan progress: %+v", v)
		}
	}
	if len(a.activity.entries) != 2 || a.activity.entries[1].Category != "update" {
		t.Fatal("missing update")
	}
	a.scanLibraryMode("missing", false)
	if a.activity.entries[2].State != "error" {
		t.Fatal("missing library reported success")
	}
}
func TestProbeWaitAndCancel(t *testing.T) {
	a := testApp(t)
	a.db.Exec("INSERT INTO libraries(id,name,path,kind) VALUES('l','test','/media/test','movies')")
	a.db.Exec("INSERT INTO items(id,lib,parent,name,kind,path,url,seen) VALUES('i','l','l','movie','Movie','/media/test/a.strm','https://example.com/a','g')")
	a.probes.running = 1
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		a.probeMedia(httptest.NewRecorder(), httptest.NewRequest("POST", "/", nil).WithContext(ctx), User{Admin: true}, "i")
	}()
	deadline := time.Now().Add(time.Second)
	for {
		w := httptest.NewRecorder()
		a.activitySnapshot(w, httptest.NewRequest("GET", "/", nil))
		var b struct{ ProbeWaiting int }
		json.Unmarshal(w.Body.Bytes(), &b)
		if b.ProbeWaiting == 1 {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			a.probes.mu.Lock()
			a.probes.running = 0
			a.dispatchProbesLocked(1)
			a.probes.mu.Unlock()
			wg.Wait()
			t.Fatal("request did not wait")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	wg.Wait()
	a.probes.mu.Lock()
	a.probes.running = 0
	a.dispatchProbesLocked(1)
	a.probes.mu.Unlock()
	a.probes.mu.Lock()
	j := a.probes.jobs["i:"+digest("https://example.com/a")]
	a.probes.mu.Unlock()
	if j != nil {
		<-j.done
	}
	// HTTP cancellation must not discard a shared background extraction job.
}
func TestPlaybackClientIPAndDedupe(t *testing.T) {
	a := testApp(t)
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "172.18.0.1:1234"
	r.Header.Set("X-Forwarded-For", "192.0.2.9, 203.0.113.5")
	r.Header.Set("X-Emby-Authorization", `Emby Client="Test", Device="TV"`)
	u := User{Name: "Alice", Device: "device"}
	x := Item{ID: "movie", Name: "Film"}
	a.logPlayback(r, u, x)
	a.logPlayback(r, u, x)
	if len(a.activity.entries) != 1 || a.activity.entries[0].IP != "203.0.113.5" || a.activity.entries[0].Device != "TV (device)" {
		t.Fatal(a.activity.entries)
	}
	r.RemoteAddr = "198.51.100.1:1234"
	a.logPlayback(r, u, x)
	if a.activity.entries[1].IP != "198.51.100.1" {
		t.Fatal("trusted external spoof")
	}
}
