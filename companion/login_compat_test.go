package main

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLoginSessionClientFields(t *testing.T) {
	a := testApp(t)
	r := httptest.NewRequest("POST", "/emby/Users/AuthenticateByName", strings.NewReader(`{"Username":"admin","Pw":"test-password-12345"}`))
	r.Header.Set("X-Emby-Authorization", `MediaBrowser Token="", UserId="8DAD0135-BC1E-4B7C-A6B4-79E10A3E5A94", Client="Lenna", Device="iPhone", DeviceId="lenna-test", Version="1.0.16"`)
	w := httptest.NewRecorder()
	a.serve(w, r)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var result struct {
		AccessToken string
		User        struct{ Id string }
		SessionInfo struct {
			Client, ApplicationVersion, DeviceName, DeviceId, UserName string
			PlayState                                                  map[string]any
			AdditionalUsers                                            []any
		}
	}
	if e := json.Unmarshal(w.Body.Bytes(), &result); e != nil {
		t.Fatal(e)
	}
	s := result.SessionInfo
	if s.Client != "Lenna" || s.ApplicationVersion != "1.0.16" || s.DeviceName != "iPhone" || s.PlayState == nil || s.AdditionalUsers == nil {
		t.Fatalf("incomplete session: %+v", s)
	}
	for _, p := range []string{"/emby/Items/Counts", "/emby/Users/" + result.User.Id + "/Views"} {
		r := httptest.NewRequest("GET", p, nil)
		r.Header.Set("X-Emby-Authorization", `Emby Token="`+result.AccessToken+`"`)
		w := httptest.NewRecorder()
		a.serve(w, r)
		if w.Code != 200 {
			t.Fatal(p, w.Code)
		}
	}
	w = httptest.NewRecorder()
	a.serve(w, httptest.NewRequest("GET", "/emby/Items/Counts", nil))
	if w.Code != 401 {
		t.Fatal("anonymous counts must remain protected")
	}
}

func TestPublicManifest(t *testing.T) {
	a := testApp(t)
	r := httptest.NewRequest("GET", "/emby/web/manifest.json", nil)
	r.Header.Set("Origin", "emby-local://app")
	w := httptest.NewRecorder()
	a.serve(w, r)
	if w.Code != 200 || !json.Valid(w.Body.Bytes()) || w.Header().Get("Access-Control-Allow-Origin") != "emby-local://app" {
		t.Fatal(w.Code, w.Body.String(), w.Header())
	}
}
