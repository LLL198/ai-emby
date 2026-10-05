package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestVersionGroupingAndDisplay(t *testing.T) {
	a := testApp(t)
	for _, l := range []string{"a", "b"} {
		if _, e := a.db.Exec("INSERT INTO libraries(id,name,path,kind) VALUES(?,?,?,'movies')", l, l, "/media/"+l); e != nil {
			t.Fatal(e)
		}
	}
	for _, v := range []struct{ id, lib, path string }{{"a1", "a", "/media/a/Film{tmdb-123}/1080.strm"}, {"a2", "a", "/media/a/Film{tmdb-123}/2160.strm"}, {"b1", "b", "/media/b/Film{tmdb-123}/4k.strm"}, {"b2", "b", "/media/b/Other{tmdb-456}/4k.strm"}} {
		if _, e := a.db.Exec("INSERT INTO items(id,lib,parent,name,kind,path,url,year,seen) VALUES(?,?,?,'Film','Movie',?,'http://origin/test.mkv',2020,'g')", v.id, v.lib, v.lib, v.path); e != nil {
			t.Fatal(e)
		}
	}
	x, _ := a.item("a1")
	if len(a.versions(x)) != 3 {
		t.Fatal("cross-library defaults")
	}
	where, args := a.mergeWhere("kind='Movie'", nil)
	var count int
	if e := a.db.QueryRow("SELECT count(*) FROM items WHERE "+where, args...).Scan(&count); e != nil || count != 2 {
		t.Fatal(count, e)
	}
	where, args = a.mergeWhere("lib=?", []any{"b"})
	if e := a.db.QueryRow("SELECT count(*) FROM items WHERE "+where, args...).Scan(&count); e != nil || count != 2 {
		t.Fatal("library representatives", count, e)
	}
	r := httptest.NewRequest("GET", "http://server:7799/Items/a1", nil)
	for _, m := range a.versionSources(x, r, User{}, true) {
		if !strings.HasPrefix(m["Path"].(string), "http://server:7799/emby/Videos/") || m["DirectStreamUrl"] == nil {
			t.Fatal("missing authenticated display playback URL")
		}
	}
	if a.versionSources(x, r, User{}, false)[0]["Path"] == nil {
		t.Fatal("missing playback path")
	}
	w := httptest.NewRecorder()
	a.enhancementSettings(w, httptest.NewRequest("PUT", "/admin/enhancements", strings.NewReader(`{"MergeVersionsAcrossLibraries":false}`)))
	if w.Code != 200 || len(a.versions(x)) != 2 || !a.defaultOn("merge_versions_folder") {
		t.Fatal("folder toggle")
	}
	w = httptest.NewRecorder()
	a.enhancementSettings(w, httptest.NewRequest("PUT", "/admin/enhancements", strings.NewReader(`{"MergeVersionsInFolder":false}`)))
	if w.Code != 200 || len(a.versions(x)) != 1 {
		t.Fatal("disabled merge")
	}
	for _, pair := range [][2]int{{1, 1}, {1, 2}, {2, 1}} {
		var k string
		if e := a.db.QueryRow("SELECT media_version_key_v2('Episode','/media/Show{tmdb-7}/s','Episode',2020,?,?, 'x')", pair[0], pair[1]).Scan(&k); e != nil {
			t.Fatal(e)
		}
		if !strings.HasSuffix(k, map[[2]int]string{{1, 1}: "1:1", {1, 2}: "1:2", {2, 1}: "2:1"}[pair]) {
			t.Fatal(k)
		}
	}
}
func TestPlaybackLogStableDeviceConcurrent(t *testing.T) {
	a := testApp(t)
	u := User{Name: "Alice", Device: "browser"}
	x := Item{ID: "film"}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r := httptest.NewRequest("GET", "/Videos/film/stream", nil)
			if i%2 == 0 {
				r.Header.Set("X-Emby-Authorization", `Emby Client="Web", Device="Browser"`)
			}
			a.logPlayback(r, u, x)
		}(i)
	}
	wg.Wait()
	if len(a.activity.entries) != 1 {
		t.Fatal("duplicate playback log", len(a.activity.entries))
	}
	a.logPlayback(httptest.NewRequest("GET", "/Videos/other/stream?GoEmbyProbe=true", nil), u, Item{ID: "other"})
	if len(a.activity.entries) != 1 {
		t.Fatal("probe logged as playback")
	}
}
func TestNanShareResolveOnce(t *testing.T) {
	for _, status := range []int{302, 500, 200} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var requests atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.Header.Get("X-Emby-Config-Id") != "goembystrm2026" {
					t.Error("missing resolver config")
				}
				w.Header().Set("Location", "https://cdn.invalid/film.mkv")
				w.WriteHeader(status)
			}))
			defer upstream.Close()
			t.Setenv("NANSHARE_URL", upstream.URL)
			a := &App{}
			w := httptest.NewRecorder()
			a.resolveNanShare(w, httptest.NewRequest("GET", "/emby/Videos/x/stream.mkv", nil))
			expected := status
			if status == 200 {
				expected = 502
			}
			if w.Code != expected || requests.Load() != 1 {
				t.Fatal(w.Code, requests.Load())
			}
		})
	}
}

func TestResolverListIncludesSource(t *testing.T) {
	a := testApp(t)
	x := Item{ID: "movie", Kind: "Movie", Path: "/media/movie.strm", URL: "http://origin/movie.mkv"}
	r := httptest.NewRequest("GET", "/Items?Ids=movie&Fields=Path,MediaSources", nil)
	m := a.listDTO(x, r, User{API: true})
	sources, ok := m["MediaSources"].([]M)
	if !ok || len(sources) != 1 || sources[0]["Path"] != x.URL {
		t.Fatal("NanShare cannot resolve the STRM source", m)
	}
	viewer := a.listDTO(x, r, User{})
	if viewer["MediaSources"] == nil || viewer["Path"] != nil {
		t.Fatal("source exposed to viewer")
	}
}
