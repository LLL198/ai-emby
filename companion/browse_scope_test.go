package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBrowseExtractionScope(t *testing.T) {
	for _, tc := range []struct{ path, want string }{
		{"/Shows/s/Episodes?Fields=MediaSources,Overview", ""},
		{"/Shows/s/Episodes?Fields=MediaSources,Overview&Limit=1", ""},
		{"/Items?Ids=e1,e2&Fields=MediaSources,Overview", ""},
		{"/Items?Ids=e1,e2&Fields=MediaSources,Overview&Limit=1", ""},
		{"/Items?Ids=e1&Fields=MediaSources,Overview", "e1"},
		{"/Items/e1", "e1"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			a, _, _ := catalogFixture(t)
			a.db.Exec("UPDATE items SET url=url || id")
			a.probes.running = 1
			w := browseGet(a, tc.path, true)
			if w.Code != 200 {
				t.Fatalf("status %d", w.Code)
			}
			if tc.want == "" {
				if len(a.probes.queue) != 0 {
					t.Fatal("list queued extraction")
				}
			} else if len(a.probes.queue) != 1 || a.probes.queue[0].x.ID != tc.want {
				t.Fatal("detail must queue exactly the selected item")
			}
		})
	}
}

func TestDetailDoesNotProbeAlternateVersions(t *testing.T) {
	a := testApp(t)
	x := probeFixture(t, a)
	_, err := a.db.Exec("UPDATE items SET path='/media/Film{tmdb-123}/one.strm' WHERE id=?", x.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.db.Exec("INSERT INTO items(id,lib,parent,name,kind,path,url,seen) VALUES('alternate','l','l','movie','Movie','/media/Film{tmdb-123}/two.strm','https://example.org/alternate','g')")
	if err != nil {
		t.Fatal(err)
	}
	x, _ = a.item(x.ID)
	if len(a.versions(x)) != 2 {
		t.Fatal("fixture must have two versions")
	}
	a.probes.running = 1
	a.single(httptest.NewRecorder(), httptest.NewRequest("GET", "/Items/"+x.ID, nil), User{}, x.ID)
	if len(a.probes.queue) != 1 || a.probes.queue[0].x.ID != x.ID {
		t.Fatal("alternate version was probed")
	}
}

func TestProbeRequestDoesNotChainNext(t *testing.T) {
	a := testApp(t)
	a.scheduleNext(Item{ID: "probe-episode", Kind: "Episode"}, httptest.NewRequest("GET", "/Videos/probe-episode/stream?GoEmbyProbe=true", nil))
	a.probes.mu.Lock()
	defer a.probes.mu.Unlock()
	if len(a.probes.next) != 0 {
		t.Fatal("probe scheduled another episode")
	}
}

func TestNextRequiresSuccessfulPlaybackRedirect(t *testing.T) {
	for _, tc := range []struct {
		method string
		status int
		want   bool
	}{
		{http.MethodHead, 302, false},
		{http.MethodGet, 403, false},
		{http.MethodGet, 502, false},
		{http.MethodGet, 302, true},
	} {
		t.Run(tc.method+http.StatusText(tc.status), func(t *testing.T) {
			a := testApp(t)
			x := probeFixture(t, a)
			if _, e := a.db.Exec("UPDATE items SET kind='Episode' WHERE id=?", x.ID); e != nil {
				t.Fatal(e)
			}
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", "https://example.org/video")
				w.WriteHeader(tc.status)
			}))
			defer s.Close()
			t.Setenv("NANSHARE_URL", s.URL)
			var uid string
			if e := a.db.QueryRow("SELECT id FROM users LIMIT 1").Scan(&uid); e != nil {
				t.Fatal(e)
			}
			w := httptest.NewRecorder()
			a.stream(w, httptest.NewRequest(tc.method, "/Videos/"+x.ID+"/stream", nil), User{API: false, ID: uid, Device: "test", Max: 2}, x.ID)
			if w.Code != tc.status {
				t.Fatalf("status %d want %d", w.Code, tc.status)
			}
			a.probes.mu.Lock()
			got := len(a.probes.next) > 0
			a.probes.mu.Unlock()
			if got != tc.want {
				t.Fatal("unexpected next episode scheduling", got)
			}
			// Cancel the pending callback before the fixture database closes.
			cfg := a.probeSettings()
			cfg.PreloadNext = false
			putProbeSettings(t, a, cfg)
		})
	}
}
