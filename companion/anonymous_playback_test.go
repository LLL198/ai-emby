package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestAnonymousPlaybackRoutesNeverReachProvider(t *testing.T) {
	a, _, source, calls := cloudProtectionFixture(t)
	parsed, err := url.Parse(source)
	if err != nil {
		t.Fatal(err)
	}
	paths := []string{
		"/Videos/movie/stream.mkv", "/Videos/movie/original",
		"/Videos/movie/master.m3u8", "/Audio/movie/universal",
		"/Items/movie/Playback/stream.mkv", "/Items/movie/PlaybackInfo",
		"/emby/Videos/movie/stream.mkv", "/emby/emby/Videos/movie/stream.mkv",
		"//Videos//movie//stream.mkv", "/vIdEoS/movie/sTrEaM.mkv",
		"/%56ideos/movie/stream.mkv", "/Videos%2fmovie%2fstream.mkv",
		"/web/../Videos/movie/stream.mkv", "/Videos/movie/stream.mkv/",
		"/Videos/movie/stream.mkv;public", "/Items/movie",
		"/Auth/Keys", "/Users", "/Users/Me", "/Sessions",
		"/api/files?path=/", "/api/files?path=%2F..%2Fapp%2Fdata",
		"/Videos/movie/Subtitles/0/Stream.srt", "/admin/keys", "/admin/logs",
	}
	for _, path := range paths {
		for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPost} {
			r := httptest.NewRequest(method, path, strings.NewReader(`{}`))
			r.Header.Set("X-Go-Emby-Internal", "true")
			r.Header.Set("X-Forwarded-For", "127.0.0.1")
			r.Header.Set("X-Emby-User-Id", "admin")
			r.Header.Set("Range", "bytes=0-0")
			w := httptest.NewRecorder()
			a.serve(w, r)
			if w.Code < 400 || w.Header().Get("Location") != "" || *calls != 0 {
				t.Fatalf("anonymous %s %s reached playback: HTTP %d", method, path, w.Code)
			}
			if strings.Contains(w.Body.String(), "/cloud/resolve/") || strings.Contains(w.Body.String(), "viewer-token") {
				t.Fatal("anonymous response disclosed a playback credential")
			}
		}
	}
	for i := 0; i < 1000; i++ {
		method := http.MethodGet
		if i%2 == 1 {
			method = http.MethodHead
		}
		r := httptest.NewRequest(method, source, nil)
		r.RemoteAddr = "127.0.0.1:3210"
		r.Header.Set("X-Go-Emby-Internal", "true")
		r.Header.Set("X-Forwarded-For", "127.0.0.1")
		if i%3 != 0 {
			digest := sha256.Sum256([]byte(fmt.Sprintf("anonymous-probe-%d", i)))
			r.Header.Set("X-Emby-Token", hex.EncodeToString(digest[:]))
		}
		if i%5 == 0 {
			q := parsed.Query()
			q.Set("internal_token", q.Get("sign"))
			q.Set("internal_expires", fmt.Sprint(time.Now().Add(time.Hour).Unix()))
			r.URL.RawQuery = q.Encode()
		}
		w := httptest.NewRecorder()
		a.cloudResolve(w, r)
		if w.Code != http.StatusUnauthorized || w.Header().Get("Location") != "" || *calls != 0 {
			t.Fatalf("anonymous signed STRM probe %d reached the provider: HTTP %d", i, w.Code)
		}
	}
	var plays int
	if err := a.db.QueryRow("SELECT count(*) FROM plays").Scan(&plays); err != nil || plays != 0 {
		t.Fatal("anonymous probes created a playback reservation", plays, err)
	}
	// The source is usable: an authenticated positive control must still work.
	w := httptest.NewRecorder()
	a.serve(w, httptest.NewRequest("GET", "/Videos/movie/stream.mkv?api_key=viewer-token", nil))
	if w.Code != http.StatusOK || w.Body.String() != "video" || *calls == 0 {
		t.Fatal("anonymous rejection was caused by an unusable test source")
	}
	t.Log("1075 anonymous probes rejected before reaching the provider; authorized control succeeded")
}
