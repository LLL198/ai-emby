package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestErrorResponsePreservesBody(t *testing.T) {
	out := httptest.NewRecorder()
	w := &errorResponse{ResponseWriter: out, status: 200}
	w.WriteHeader(502)
	body := strings.Repeat("x", 3000)
	w.Write([]byte(body))
	if out.Code != 502 || out.Body.String() != body || w.detail.Len() != 2048 {
		t.Fatal("response altered or error capture unbounded")
	}
}
func TestPlaybackErrorActivity(t *testing.T) {
	a := testApp(t)
	r := httptest.NewRequest("GET", "/Videos/missing/stream?api_key=private-test-secret", nil)
	w := httptest.NewRecorder()
	a.serve(w, r)
	if w.Code < 400 {
		t.Fatal("expected failure")
	}
	found := false
	for _, v := range a.activity.entries {
		if v.Category == "error" {
			found = true
			if strings.Contains(v.Error, "private-test-secret") {
				t.Fatal("credential logged")
			}
		}
	}
	if !found {
		t.Fatal("playback error absent from activity")
	}
}
func TestSuccessfulResponseNotCaptured(t *testing.T) {
	out := httptest.NewRecorder()
	w := &errorResponse{ResponseWriter: out, status: 200}
	w.Header().Set("Location", "https://example.test/video")
	w.WriteHeader(http.StatusFound)
	if out.Code != 302 || out.Header().Get("Location") != "https://example.test/video" || w.detail.Len() != 0 {
		t.Fatal("redirect changed")
	}
}
