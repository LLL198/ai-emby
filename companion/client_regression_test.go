package main

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOfficialClientPreflight(t *testing.T) {
	a := &App{}
	r := httptest.NewRequest("OPTIONS", "/emby/Users/u/Items/Latest", nil)
	r.Header.Set("Origin", "emby-local://app")
	r.Header.Set("Access-Control-Request-Private-Network", "true")
	w := httptest.NewRecorder()
	a.serve(w, r)
	if w.Code != 204 || w.Header().Get("Access-Control-Allow-Origin") != "emby-local://app" || w.Header().Get("Access-Control-Allow-Credentials") != "true" || w.Header().Get("Access-Control-Allow-Private-Network") != "true" {
		t.Fatal(w.Code, w.Header())
	}
	if !strings.Contains(w.Header().Get("Access-Control-Allow-Headers"), "X-Emby-Language") {
		t.Fatal("language header missing")
	}
}

func TestViewerRedirectNeverQueuesProbe(t *testing.T) {
	a := testApp(t)
	x := probeFixture(t, a)
	a.probes.running = 1
	var uid string
	if e := a.db.QueryRow("SELECT id FROM users LIMIT 1").Scan(&uid); e != nil {
		t.Fatal(e)
	}
	t.Setenv("PUBLIC_PLAYBACK_URL", "")
	t.Setenv("NANSHARE_URL", "")
	for _, method := range []string{"GET", "HEAD"} {
		w := httptest.NewRecorder()
		a.stream(w, httptest.NewRequest(method, "/Videos/i/stream?api_key=viewer", nil), User{ID: uid, Device: "redirect-test", Max: 5}, x.ID)
		if w.Code != 302 || w.Header().Get("Location") != x.URL || len(a.probes.queue) != 0 {
			t.Fatal(w.Code, w.Header(), len(a.probes.queue))
		}
	}
}
