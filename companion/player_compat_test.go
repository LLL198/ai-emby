package main

import (
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestPlayerDiscovery(t *testing.T) {
	a := testApp(t)
	for _, path := range []string{"/System/Ping", "/emby/system/ping"} {
		for _, method := range []string{"GET", "POST"} {
			w := httptest.NewRecorder()
			a.serve(w, httptest.NewRequest(method, path, nil))
			if w.Code != 200 || w.Body.String() != "Emby Server" {
				t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
			}
		}
	}
	w := httptest.NewRecorder()
	a.serve(w, httptest.NewRequest("OPTIONS", "/emby/Users/AuthenticateByName", nil))
	if w.Code != 204 || w.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Fatal(w)
	}
}
func TestPlayerAuthorization(t *testing.T) {
	for _, raw := range []string{`MediaBrowser Client="SenPlayer", Token = "viewer", DeviceId = "phone"`, `Emby Token="viewer", DeviceId="phone"`} {
		r := httptest.NewRequest("GET", "/Items", nil)
		r.Header.Set("Authorization", raw)
		if token(r) != "viewer" || device(r) != "phone" {
			t.Fatal("header auth not parsed")
		}
		r = httptest.NewRequest("GET", "/Items?X-Emby-Authorization="+url.QueryEscape(raw), nil)
		if token(r) != "viewer" || device(r) != "phone" {
			t.Fatal("query auth not parsed")
		}
	}
}
func TestPlayerDirectPlayUsesAuthenticatedGateway(t *testing.T) {
	a := testApp(t)
	x := Item{ID: "movie", Kind: "Movie", URL: "http://private-origin/movie.mp4"}
	r := httptest.NewRequest("GET", "http://server:7799/Items/movie/PlaybackInfo?api_key=viewer", nil)
	m := a.viewerSource(x, r, User{})
	if m["SupportsDirectPlay"] != true || m["SupportsTranscoding"] != false {
		t.Fatal("wrong playback capability")
	}
	u, err := url.Parse(m["Path"].(string))
	if err != nil || u.Host != "server:7799" || u.Query().Get("api_key") != "viewer" {
		t.Fatal("direct play bypasses gateway")
	}
}

func TestLibraryDiscoveryTypes(t *testing.T) {
	a := testApp(t)
	if a.rootDTO()["Type"] != "UserRootFolder" {
		t.Fatal("wrong root type")
	}
	if _, e := a.db.Exec("INSERT INTO libraries(id,name,path,kind) VALUES('l','Library','/media/test','movies')"); e != nil {
		t.Fatal(e)
	}
	for _, path := range []string{"/Items?IncludeItemTypes=CollectionFolder,UserView", "/Items?UserId=viewer", "/Users/viewer/Items"} {
		w := httptest.NewRecorder()
		a.items(w, httptest.NewRequest("GET", path, nil), User{}, false)
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"CollectionType":"movies"`) {
			t.Fatalf("%s: %s", path, w.Body.String())
		}
	}
}
