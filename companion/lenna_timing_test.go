package main

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestLennaLibraryTypes(t *testing.T) {
	a, uid, _ := catalogFixture(t)
	for _, tc := range []struct {
		library, parent, kind, client, want string
		count                               int
	}{
		{"movies", "l", "movie", "Lenna/1.0.16", "Movie", 3},
		{"tvshows", "l", "movie", "Lenna/1.0.16", "Series", 1},
		{"tvshows", "l", "series", "Other/1", "Series", 1},
		{"tvshows", "s", "season", "Lenna/1.0.16", "Season", 2},
		{"tvshows", "s1", "episode", "Lenna/1.0.16", "Episode", 1},
		{"tvshows", "l", "movie", "Other/1", "Movie", 3},
	} {
		t.Run(tc.client+tc.library+tc.kind, func(t *testing.T) {
			if _, err := a.db.Exec("UPDATE libraries SET kind=? WHERE id='l'", tc.library); err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest("GET", "/emby/Users/"+uid+"/Items?Recursive=true&ParentId="+tc.parent+"&IncludeItemTypes="+tc.kind+"&SortBy=Random&Limit=60", nil)
			r.Header.Set("X-Emby-Token", "browse-test")
			r.Header.Set("User-Agent", tc.client)
			w := httptest.NewRecorder()
			a.serve(w, r)
			var b struct {
				Items            []struct{ Type string }
				TotalRecordCount int
			}
			if err := json.Unmarshal(w.Body.Bytes(), &b); err != nil || w.Code != 200 {
				t.Fatalf("%d %s", w.Code, w.Body.String())
			}
			if len(b.Items) != tc.count {
				t.Fatalf("got %d want %d: %s", len(b.Items), tc.count, w.Body.String())
			}
			for _, x := range b.Items {
				if x.Type != tc.want {
					t.Fatalf("got %s want %s", x.Type, tc.want)
				}
			}
		})
	}
}

func TestBrowseProbeIndependentOfPlayback(t *testing.T) {
	for _, browse := range []bool{true, false} {
		t.Run(map[bool]string{true: "enabled", false: "disabled"}[browse], func(t *testing.T) {
			a := testApp(t)
			x := probeFixture(t, a)
			a.probes.running = 1
			cfg := a.probeSettings()
			cfg.Browse = browse
			cfg.PreloadNext = false
			if putProbeSettings(t, a, cfg) != 200 {
				t.Fatal("settings")
			}
			var uid string
			a.db.QueryRow("SELECT id FROM users LIMIT 1").Scan(&uid)
			if _, err := a.db.Exec("INSERT INTO tokens VALUES(?,?,?,?)", digest("timing"), uid, "timing", time.Now().Unix()+600); err != nil {
				t.Fatal(err)
			}
			call := func(method, path, body string) {
				r := httptest.NewRequest(method, path, strings.NewReader(body))
				r.Header.Set("X-Emby-Token", "timing")
				w := httptest.NewRecorder()
				a.serve(w, r)
				if w.Code >= 400 {
					t.Fatalf("%s %d %s", path, w.Code, w.Body.String())
				}
			}
			call("GET", "/emby/Users/"+uid+"/Items/"+x.ID, "")
			want := 0
			if browse {
				want = 1
			}
			if len(a.probes.queue) != want {
				t.Fatal("browse queue", len(a.probes.queue))
			}
			// Clear pending jobs to prove playback cannot enqueue even with an empty cache.
			a.probes.queue = nil
			a.probes.jobs = nil
			call("POST", "/emby/Items/"+x.ID+"/PlaybackInfo", "{}")
			call("POST", "/emby/Sessions/Playing", `{"ItemId":"i","PositionTicks":0}`)
			call("POST", "/emby/Sessions/Playing/Progress", `{"ItemId":"i","PositionTicks":10000000}`)
			time.Sleep(50 * time.Millisecond)
			a.probes.mu.Lock()
			defer a.probes.mu.Unlock()
			if len(a.probes.queue) != 0 {
				t.Fatal("playback triggered extraction")
			}
		})
	}
}
