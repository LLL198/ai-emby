package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func headTestClient() *http.Client {
	return &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func TestSTRMHeadAcceptsGETSignedCDN(t *testing.T) {
	var cdnCalls atomic.Int32
	const signature = "signature=a%2Bb%2Fc%3D"
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cdnCalls.Add(1)
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if r.Method != http.MethodGet || r.URL.RawQuery != signature || r.Header.Get("Range") != "bytes=0-0" || r.UserAgent() != "test-player" {
			t.Error("signed source request changed")
		}
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" || r.Header.Get("X-Emby-Token") != "" {
			t.Error("viewer credentials leaked to CDN")
		}
		w.Header().Set("Content-Type", "video/x-matroska")
		w.Header().Set("Content-Range", "bytes 0-0/123456789")
		w.Header().Set("Content-Length", "1")
		w.WriteHeader(http.StatusPartialContent)
		w.Write([]byte{0})
	}))
	defer cdn.Close()
	resolver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Error("resolver request changed or contained credentials")
		}
		w.Header().Set("Location", cdn.URL+"/movie.mkv?"+signature)
		w.WriteHeader(http.StatusFound)
	}))
	defer resolver.Close()
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodHead || r.Header.Get("X-Emby-Token") != "viewer" || r.Host != "panel.example" {
			t.Error("core authentication or viewer host changed")
		}
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Location", resolver.URL+"/resolve")
		w.WriteHeader(http.StatusFound)
	}))
	defer core.Close()
	r := httptest.NewRequest(http.MethodHead, "http://panel.example/emby/Videos/i/stream.mkv?api_key=viewer", nil)
	r.Header.Set("User-Agent", "test-player")
	r.Header.Set("Authorization", "Bearer viewer")
	r.Header.Set("Cookie", "session=viewer")
	w := httptest.NewRecorder()
	serveSTRMHeadFrom(w, r, core.URL, headTestClient())
	if w.Code != 200 || w.Header().Get("Content-Length") != "123456789" || w.Header().Get("Accept-Ranges") != "bytes" || w.Header().Get("Location") != "" || w.Body.Len() != 0 || w.Header().Get("Content-Type") != "video/x-matroska" {
		t.Fatalf("invalid HEAD result: status=%d headers=%v body=%d", w.Code, w.Header(), w.Body.Len())
	}
	if w.Header().Get("Access-Control-Allow-Origin") != "*" || w.Header().Get("X-AI-Emby-Head-Compatibility") != "range-get" || cdnCalls.Load() != 1 {
		t.Fatal("missing compatibility headers or extra CDN requests")
	}
}

func TestSTRMHeadPreservesAuthenticationFailure(t *testing.T) {
	var sourceCalls atomic.Int32
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sourceCalls.Add(1)
		w.WriteHeader(200)
	}))
	defer source.Close()
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", source.URL)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer core.Close()
	w := httptest.NewRecorder()
	serveSTRMHeadFrom(w, httptest.NewRequest("HEAD", "/Videos/i/stream", nil), core.URL, headTestClient())
	if w.Code != 401 || sourceCalls.Load() != 0 {
		t.Fatal("unauthorized request contacted source or changed status")
	}
}

func TestSTRMHeadFallsBackOnUnavailableSource(t *testing.T) {
	for _, mode := range []string{"forbidden", "json", "invalid-range", "relative"} {
		t.Run(mode, func(t *testing.T) {
			source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if mode == "relative" && r.URL.Path == "/resolve" {
					w.Header().Set("Location", "/file?signature=a%2Bb")
					w.WriteHeader(302)
					return
				}
				w.Header().Set("Content-Type", "application/octet-stream")
				switch mode {
				case "forbidden":
					w.WriteHeader(403)
				case "json":
					w.Header().Set("Content-Type", "application/json")
					w.Write([]byte(`{"error":"unavailable"}`))
				case "invalid-range":
					w.Header().Set("Content-Range", "bytes 0-0/*")
					w.WriteHeader(206)
				default:
					if r.URL.RawQuery != "signature=a%2Bb" {
						t.Error("relative redirect query changed")
					}
					w.Header().Set("Content-Length", "123")
					w.WriteHeader(200)
				}
			}))
			defer source.Close()
			core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", source.URL+"/resolve")
				w.WriteHeader(302)
			}))
			defer core.Close()
			w := httptest.NewRecorder()
			serveSTRMHeadFrom(w, httptest.NewRequest("HEAD", "/Videos/i/stream", nil), core.URL, headTestClient())
			if mode == "relative" {
				if w.Code != 200 || w.Header().Get("Content-Length") != "123" {
					t.Fatal("relative redirect not resolved")
				}
			} else if w.Code != 302 || !strings.HasPrefix(w.Header().Get("Location"), source.URL) {
				t.Fatal("unavailable source must preserve the original redirect")
			}
		})
	}
}

func TestSTRMPlaybackRoutes(t *testing.T) {
	for _, path := range []string{"/Videos/i/stream.mkv", "/emby/Videos/i/original", "/emby/Items/i/Playback/stream.mkv", "/emby/emby/Videos/i/stream"} {
		if strmPlaybackItem(path) != "i" {
			t.Errorf("route not recognized: %s", path)
		}
	}
	for _, path := range []string{"/Videos/i/Subtitles/0/Stream.srt", "/Items/i/PlaybackInfo", "/Videos/i/master.m3u8", "/Videos/i/stream.mkv/extra"} {
		if strmPlaybackItem(path) != "" {
			t.Errorf("unrelated route recognized: %s", path)
		}
	}
}

func TestSTRMHeadRedirectLimit(t *testing.T) {
	var calls atomic.Int32
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Location", "/loop")
		w.WriteHeader(302)
	}))
	defer source.Close()
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", source.URL+"/loop")
		w.WriteHeader(302)
	}))
	defer core.Close()
	w := httptest.NewRecorder()
	serveSTRMHeadFrom(w, httptest.NewRequest("HEAD", "/Videos/i/stream", nil), core.URL, headTestClient())
	if calls.Load() != 5 || w.Code != 302 || w.Header().Get("X-AI-Emby-Head-Compatibility") != "" {
		t.Fatal("redirect loop was not bounded or produced false success")
	}
}

func TestSTRMHeadLeavesOtherRequestsAlone(t *testing.T) {
	for _, method := range []string{"GET", "POST", "OPTIONS", "HEAD"} {
		w := httptest.NewRecorder()
		if serveSTRMHead(nil, w, httptest.NewRequest(method, "/Videos/i/stream", nil)) || w.Body.Len() != 0 {
			t.Fatalf("unexpected interception without a STRM record: %s", method)
		}
	}
	for _, value := range []string{"", "bytes 0-0/*", "bytes 0-0/0", "bytes 0-1/100", "bytes 0-0/9223372036854775808"} {
		if playbackTotalLength(value) != -1 {
			t.Errorf("accepted invalid file length: %q", value)
		}
	}
}
