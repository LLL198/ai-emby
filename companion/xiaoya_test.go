package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestXiaoyaResolver(t *testing.T) {
	for _, tc := range []struct {
		name, body, location string
		status               int
		ok                   bool
	}{
		{"redirect", "", "https://cdn.example/video?sig=abc", 302, true},
		{"text", "https://cdn.example/video", "", 200, true},
		{"json", `{"url":"https://cdn.example/video"}`, "", 200, true},
		{"loopback", "", "http://127.0.0.1:8097/stream", 302, false},
		{"private", "", "http://172.18.0.1/stream", 302, false},
		{"relative", "", "/stream", 302, false},
		{"error", `{"code":500,"message":"error"}`, "", 200, false},
		{"html", "<html>failure</html>", "", 200, false},
		{"oversize", strings.Repeat("a", 65537), "", 200, false},
		{"upstream", "", "", 500, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.URL.Path != "/d/中文 a.mp4" || r.URL.Query().Get("sign") != "" || r.URL.Query().Get("other") != "kept" {
					t.Errorf("unexpected resolver request: %s %s", r.Method, r.URL)
				}
				if r.Header.Get("X-Emby-Token") != "" || r.Header.Get("Authorization") != "" || r.Header.Get("X-Alist-OriUA") != "test-player" {
					t.Error("incorrect headers")
				}
				if tc.location != "" {
					w.Header().Set("Location", tc.location)
				}
				w.WriteHeader(tc.status)
				w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			r := httptest.NewRequest("HEAD", "http://emby/Videos/abcdef/stream", nil)
			r.Header.Set("User-Agent", "test-player")
			r.Header.Set("X-Emby-Token", "secret")
			_, err := xiaoyaLink(r, "http://xiaoya.host:5678/d/中文%20a.mp4?sign=SIGN_STR&other=kept", srv.URL)
			if (err == nil) != tc.ok {
				t.Fatalf("ok=%v error=%v", tc.ok, err)
			}
		})
	}
}
