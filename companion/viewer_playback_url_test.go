package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestViewerPlaybackURLNeverReturnsPermanentSource(t *testing.T) {
	const source = "https://private.example/cloud/resolve/mount?path=%2Fmovie.mkv&sign=permanent-test-sign"
	const playback = "/emby/Videos/movie/stream.mkv?Static=true&api_key=viewer"
	for _, tc := range []struct {
		name, method, endpoint, remote, agent string
		forwarded                             bool
	}{
		{"private GET without client headers", "GET", "/Items/movie/PlaybackInfo", "172.18.0.2:3210", "", false},
		{"loopback GET", "GET", "/emby/Items/movie/PlaybackInfo", "127.0.0.1:3210", "", false},
		{"nginx GET", "GET", "/Items/movie/PlaybackInfo", "192.168.1.10:3210", "nginx/1.28", false},
		{"public player GET", "GET", "/Items/movie/PlaybackInfo", "203.0.113.1:3210", "Hills", false},
		{"player POST", "POST", "/Items/movie/PlaybackInfo", "172.18.0.2:3210", "", false},
		{"reverse proxy", "GET", "/Items/movie/PlaybackInfo", "172.18.0.2:3210", "", true},
		{"item details", "GET", "/Items/movie", "172.18.0.2:3210", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, "http://panel.example"+tc.endpoint+"?api_key=viewer", nil)
			r.RemoteAddr = tc.remote
			r.Header.Set("User-Agent", tc.agent)
			origin := "http://panel.example"
			if tc.forwarded {
				r.Header.Set("X-Forwarded-Proto", "https")
				r.Header.Set("X-Forwarded-Host", "media.example")
				origin = "https://media.example"
			}
			media := M{"Path": source}
			viewerSourceURL(media, playback, r, User{ID: "viewer"})
			if media["Path"] != origin+playback || media["DirectStreamUrl"] != strings.TrimPrefix(playback, "/emby") {
				t.Fatal("viewer did not receive the authenticated playback entry")
			}
			encoded, err := json.Marshal(media)
			if err != nil || strings.Contains(string(encoded), "permanent-test-sign") || strings.Contains(string(encoded), "private.example") {
				t.Fatal("permanent STRM source leaked into playback metadata")
			}
		})
	}
}

func TestServiceAPIPlaybackSourceRemainsAvailable(t *testing.T) {
	const source = "https://resolver.example/source?sign=service-only"
	media := M{"Path": source}
	r := httptest.NewRequest("GET", "http://panel.example/Items/movie/PlaybackInfo?api_key=service", nil)
	viewerSourceURL(media, "/emby/Videos/movie/stream.mkv?api_key=service", r, User{API: true})
	if media["Path"] != source {
		t.Fatal("service API source lookup changed")
	}
}

func TestCloudPlaybackMetadataHidesSTRMForPrivateClients(t *testing.T) {
	a, _, _, calls := cloudProtectionFixture(t)
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		for _, prefix := range []string{"", "/emby"} {
			r := httptest.NewRequest(method, "http://panel.example"+prefix+"/Items/movie/PlaybackInfo?api_key=viewer-token", strings.NewReader(`{}`))
			r.RemoteAddr = "172.18.0.2:3210"
			w := httptest.NewRecorder()
			a.serve(w, r)
			if w.Code != http.StatusOK {
				t.Fatalf("playback metadata HTTP %d", w.Code)
			}
			var response struct {
				MediaSources []struct{ Path, DirectStreamUrl string }
			}
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || len(response.MediaSources) != 1 {
				t.Fatal("invalid playback metadata", err)
			}
			source := response.MediaSources[0]
			u, err := url.Parse(source.Path)
			if err != nil || u.Host != "panel.example" || u.Path != "/emby/Videos/movie/stream.mkv" || u.Query().Get("api_key") != "viewer-token" || source.DirectStreamUrl == "" {
				t.Fatal("authenticated cloud playback entry missing")
			}
			if strings.Contains(w.Body.String(), "/cloud/resolve/") || strings.Contains(w.Body.String(), "internal_token") || strings.Contains(w.Body.String(), "sign=") {
				t.Fatal("STRM or internal task credential returned to player")
			}
		}
	}
	if *calls != 0 {
		t.Fatal("metadata lookup unexpectedly resolved cloud download URL")
	}
}
