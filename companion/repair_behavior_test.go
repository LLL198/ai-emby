package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestClientTicksStrings(t *testing.T) {
	for _, s := range []string{`123`, `"123"`} {
		var n clientTicks
		if e := json.Unmarshal([]byte(s), &n); e != nil || n != 123 {
			t.Fatal(s, n, e)
		}
	}
}
func TestCDNCacheSeparatedByClient(t *testing.T) {
	var count atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		w.Header().Set("Location", "https://cdn.example/movie?secret=private")
		w.WriteHeader(302)
	}))
	defer s.Close()
	t.Setenv("NANSHARE_URL", s.URL)
	a := &App{}
	for _, ua := range []string{"one", "one", "two"} {
		r := httptest.NewRequest("GET", "/Videos/cache-test/stream", nil)
		r.Header.Set("User-Agent", ua)
		w := httptest.NewRecorder()
		a.resolveNanShare(w, r)
		if w.Code != 302 {
			t.Fatal(w.Code)
		}
	}
	if count.Load() != 2 {
		t.Fatal(count.Load())
	}
}
func TestProbeTimeoutReason(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if !strings.Contains(probeFailure(ctx, context.Canceled).Error(), "超时") {
		t.Fatal("missing timeout")
	}
}
func TestDisabledNextCancelsPending(t *testing.T) {
	a := testApp(t)
	x := probeFixture(t, a)
	x.Kind = "Episode"
	cfg := a.probeSettings()
	cfg.PreloadNext = false
	putProbeSettings(t, a, cfg)
	a.scheduleNext(x, httptest.NewRequest("GET", "/", nil))
	time.Sleep(3200 * time.Millisecond)
	a.probes.mu.Lock()
	defer a.probes.mu.Unlock()
	if len(a.probes.queue) != 0 || a.probes.running != 0 {
		t.Fatal("disabled preload queued work")
	}
}
func TestIdenticalSourceDeduplicates(t *testing.T) {
	a := testApp(t)
	x := probeFixture(t, a)
	a.probes.running = 1
	one := a.queueProbe(x, "t", "ua", false)
	other := x
	other.ID = "alias"
	if two := a.queueProbe(other, "t", "ua", false); one != two {
		t.Fatal("duplicate source queued twice")
	}
	if err := a.saveMedia(x, M{"MediaStreams": []M{{"Type": "Video", "Codec": "h264"}}, "RunTimeTicks": int64(100000000)}); err != nil {
		t.Fatal(err)
	}
	if len(a.cachedMedia(other)) == 0 {
		t.Fatal("identical source cache not reused")
	}
}
func TestProgressStringsAndMissingPosition(t *testing.T) {
	a := testApp(t)
	x := probeFixture(t, a)
	var uid string
	a.db.QueryRow("SELECT id FROM users LIMIT 1").Scan(&uid)
	a.db.Exec("INSERT INTO tokens VALUES(?,?,?,?)", digest("progress-test"), uid, "progress", time.Now().Unix()+600)
	call := func(body string) {
		r := httptest.NewRequest("POST", "/Sessions/Playing/Progress", strings.NewReader(body))
		r.Header.Set("X-Emby-Token", "progress-test")
		w := httptest.NewRecorder()
		a.serve(w, r)
		if w.Code != 204 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	call(`{"ItemId":"` + x.ID + `","PositionTicks":"50000000","RunTimeTicks":"100000000"}`)
	call(`{"ItemId":"` + x.ID + `"}`)
	a.activity.mu.Lock()
	defer a.activity.mu.Unlock()
	for _, v := range a.activity.entries {
		if v.Category == "playback" {
			if v.PositionTicks != 50000000 || v.Progress != 50 {
				t.Fatal(*v)
			}
			return
		}
	}
	t.Fatal("no playback entry")
}
func TestNextWaitsForPlaybackAndDeduplicates(t *testing.T) {
	a := testApp(t)
	probeFixture(t, a)
	a.probes.running = 1
	a.db.Exec("INSERT INTO items(id,lib,parent,name,kind,path,url,season,episode,seen) VALUES('series','l','l','火影忍者','Series','/media/naruto','',0,0,'g'),('season','l','series','第一季','Season','/media/naruto/1','',1,0,'g'),('ep1','l','season','第一集','Episode','/media/naruto/1/1','https://example.com/ep1',1,1,'g'),('ep2','l','season','第二集','Episode','/media/naruto/1/2','https://example.com/ep2',1,2,'g')")
	x, e := a.item("ep1")
	if e != nil {
		t.Fatal(e)
	}
	cfg := a.probeSettings()
	cfg.Browse = false
	cfg.PreloadNext = true
	putProbeSettings(t, a, cfg)
	r := httptest.NewRequest("GET", "/Items/ep1", nil)
	a.single(httptest.NewRecorder(), r, User{}, x.ID)
	if len(a.probes.queue) != 0 {
		t.Fatal("browse preloaded next")
	}
	a.scheduleNext(x, r)
	a.scheduleNext(x, r)
	if len(a.probes.queue) != 0 {
		t.Fatal("synchronous preload")
	}
	time.Sleep(3200 * time.Millisecond)
	a.probes.mu.Lock()
	defer a.probes.mu.Unlock()
	if len(a.probes.queue) != 1 || a.probes.queue[0].x.ID != "ep2" {
		t.Fatal("next episode not queued exactly once")
	}
	if a.mediaLabel(a.probes.queue[0].x) != "火影忍者 第1季 第2集" {
		t.Fatal("episode label")
	}
}
