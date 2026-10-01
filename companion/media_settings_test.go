package main

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func probeFixture(t *testing.T, a *App) Item {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "video.mp4.strm")
	if e := os.WriteFile(path, []byte("https://example.com/opaque"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := a.db.Exec("INSERT INTO libraries(id,name,path,kind) VALUES('l','test',?,'movies')", root); e != nil {
		t.Fatal(e)
	}
	if _, e := a.db.Exec("INSERT INTO items(id,lib,parent,name,kind,path,url,seen) VALUES('i','l','l','Title','Movie',?,'https://example.com/opaque','g')", path); e != nil {
		t.Fatal(e)
	}
	x, e := a.item("i")
	if e != nil {
		t.Fatal(e)
	}
	return x
}
func putProbeSettings(t *testing.T, a *App, c ProbeSettings) int {
	t.Helper()
	b, _ := json.Marshal(c)
	w := httptest.NewRecorder()
	a.mediaSettings(w, httptest.NewRequest("PUT", "/admin/media-info", strings.NewReader(string(b))))
	return w.Code
}
func TestMediaSettingsDefaultsAndValidation(t *testing.T) {
	a := testApp(t)
	c := a.probeSettings()
	if !c.Browse || c.Persistent || !c.PreloadNext || c.Concurrency != 1 {
		t.Fatal(c)
	}
	c.Concurrency = 0
	if putProbeSettings(t, a, c) != 400 {
		t.Fatal("accepted zero concurrency")
	}
	c.Concurrency = 2
	c.Directory = "/tmp/outside"
	if putProbeSettings(t, a, c) != 400 {
		t.Fatal("accepted outside path")
	}
	c.Directory = filepath.Join(mediaInfoRoot(), "unified")
	if putProbeSettings(t, a, c) != 200 || a.probeSettings().Concurrency != 2 {
		t.Fatal("settings did not persist")
	}
}
func TestMediaPersistenceAndDeletion(t *testing.T) {
	a := testApp(t)
	x := probeFixture(t, a)
	m := M{"Container": "mp4", "MediaStreams": []M{{"Type": "Video", "Codec": "h264"}}}
	if e := a.saveMedia(x, m); e != nil {
		t.Fatal(e)
	}
	c := a.probeSettings()
	old := c.Directory
	c.Directory = filepath.Join(mediaInfoRoot(), "moved")
	if putProbeSettings(t, a, c) != 200 {
		t.Fatal("move failed")
	}
	if _, e := os.Stat(filepath.Join(old, digest(x.ID)+".json")); !os.IsNotExist(e) {
		t.Fatal("old copy retained")
	}
	if e := os.Remove(x.Path); e != nil {
		t.Fatal(e)
	}
	if e := a.cleanupMedia(); e != nil {
		t.Fatal(e)
	}
	if len(a.cachedMedia(x)) != 0 {
		t.Fatal("deleted file retained cache")
	}
	if e := os.WriteFile(x.Path, []byte(x.URL), 0600); e != nil {
		t.Fatal(e)
	}
	c.Persistent = true
	if putProbeSettings(t, a, c) != 200 {
		t.Fatal("persistent setting")
	}
	if e := a.saveMedia(x, m); e != nil {
		t.Fatal(e)
	}
	if _, e := a.db.Exec("DELETE FROM items WHERE id=?", x.ID); e != nil {
		t.Fatal(e)
	}
	if e := a.cleanupMedia(); e != nil {
		t.Fatal(e)
	}
	if a.cachedMedia(x)["Container"] != "mp4" {
		t.Fatal("archive not retained")
	}
	x.URL = "https://example.com/changed"
	if len(a.cachedMedia(x)) != 0 {
		t.Fatal("reused changed source")
	}
}
func TestProbeQueueDedupeAndConcurrency(t *testing.T) {
	a := testApp(t)
	x := probeFixture(t, a)
	bin := t.TempDir()
	t.Setenv("PROBE_PLAYBACK_URL", "http://example.com")
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	if e := os.WriteFile(filepath.Join(bin, "ffprobe"), []byte("#!/bin/sh\nsleep 0.2\nprintf '%s' '{\"format\":{\"format_name\":\"mov,mp4\",\"duration\":\"60\"},\"streams\":[{\"index\":0,\"codec_type\":\"video\",\"codec_name\":\"h264\",\"width\":1920,\"height\":1080}]}'\n"), 0700); e != nil {
		t.Fatal(e)
	}
	// Saturate the dispatcher until all three distinct tasks have entered the queue.
	a.probes.running = 1
	jobs := []*probeJob{}
	for _, key := range []string{"i", "j", "k"} {
		y := x
		y.ID = key
		if key != "i" {
			y.URL = x.URL + key
			_, e := a.db.Exec("INSERT INTO items(id,lib,parent,name,kind,path,url,seen) VALUES(?,'l','l','Title','Movie',?,?,'g')", key, x.Path+key, y.URL)
			if e != nil {
				t.Fatal(e)
			}
		}
		jobs = append(jobs, a.queueProbe(y, "token", "test", true))
	}
	if a.queueProbe(x, "token", "test", true) != jobs[0] {
		t.Fatal("did not deduplicate")
	}
	a.probes.mu.Lock()
	if len(a.probes.queue) != 3 {
		t.Fatal("incorrect queue")
	}
	a.probes.running = 0
	a.dispatchProbesLocked(2)
	if a.probes.running != 2 || len(a.probes.queue) != 1 {
		t.Fatal("concurrency not respected")
	}
	a.probes.mu.Unlock()
	for _, j := range jobs {
		select {
		case <-j.done:
			if j.err != nil {
				t.Fatal(j.err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("queue stalled")
		}
	}
	if a.cachedMedia(x)["Container"] != "mp4" {
		t.Fatal("probe not stored")
	}
}
func TestAPIKeyDoesNotOverrideViewerToken(t *testing.T) {
	r := httptest.NewRequest("GET", "/Items?api_key=viewer", nil)
	r.Header.Set("X-Emby-Api-Key", "upstream-key")
	if token(r) != "viewer" {
		t.Fatal("upstream key overrides viewer")
	}
	r = httptest.NewRequest("GET", "/Items", nil)
	r.Header.Set("X-Emby-Api-Key", "client-key")
	if token(r) != "client-key" {
		t.Fatal("client key unsupported")
	}
	a := testApp(t)
	x := Item{ID: "i", Name: "Title", Path: "/media/video.mp4.strm", URL: "https://example.com/opaque"}
	if !strings.Contains(a.playURL(x, "viewer"), "stream.mp4?") {
		t.Fatal("STRM original container lost")
	}
}

func TestAutomaticProbeTriggers(t *testing.T) {
	a := testApp(t)
	x := probeFixture(t, a)
	a.probes.running = 1
	r := httptest.NewRequest("GET", "/Users/viewer/Items/i?api_key=viewer", nil)
	a.single(httptest.NewRecorder(), r, User{}, x.ID)
	if len(a.probes.queue) != 1 {
		t.Fatal("browsing must queue full extraction")
	}
	c := a.probeSettings()
	c.Browse = true
	if putProbeSettings(t, a, c) != 200 {
		t.Fatal("settings failed")
	}
	a.single(httptest.NewRecorder(), r, User{}, x.ID)
	if len(a.probes.queue) != 1 {
		t.Fatal("enabled browsing did not queue")
	}

}
func TestPreloadNextCrossSeason(t *testing.T) {
	a := testApp(t)
	probeFixture(t, a)
	a.probes.running = 1
	for _, v := range []struct {
		id, parent, kind string
		season, episode  int
	}{{"series", "l", "Series", 0, 0}, {"s1", "series", "Season", 1, 1}, {"s2", "series", "Season", 2, 2}, {"e1", "s1", "Episode", 1, 9}, {"e2", "s2", "Episode", 2, 1}, {"e3", "s2", "Episode", 2, 2}} {
		if _, e := a.db.Exec("INSERT INTO items(id,lib,parent,name,kind,path,url,season,episode,seen) VALUES(?,'l',?,?,?,?,'https://example.com/video',?,?,'g')", v.id, v.parent, v.id, v.kind, "/media/"+v.id, v.season, v.episode); e != nil {
			t.Fatal(e)
		}
	}
	x, e := a.item("e1")
	if e != nil {
		t.Fatal(e)
	}
	a.preloadNext(x, httptest.NewRequest("GET", "/?api_key=viewer", nil))
	if len(a.probes.queue) != 1 || a.probes.queue[0].x.ID != "e2" {
		t.Fatal("must preload only next episode across seasons")
	}
}
