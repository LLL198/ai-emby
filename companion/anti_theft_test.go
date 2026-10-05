package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/LLL198/ai-emby/security"
)

func TestAntiTheftLoginSessionAndUnban(t *testing.T) {
	a, _, _, calls := cloudProtectionFixture(t)
	a.db.Exec("UPDATE users SET hash=(SELECT hash FROM users WHERE admin=1 LIMIT 1) WHERE id='viewer'")
	c := security.Defaults()
	c.RateLimitEnabled = true
	c.RequestsPerMinute = 1
	c.Action = "permanent"
	if err := a.saveFeatureSetting("anti-theft", c); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := security.Admit(context.Background(), a.db.DB, "viewer", true, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{"/Users/Me", "/Videos/movie/stream.mkv"} {
		w := httptest.NewRecorder()
		a.serve(w, httptest.NewRequest("GET", path+"?api_key=viewer-token", nil))
		if w.Code != 403 || *calls != 0 {
			t.Fatal("banned session still accepted", path, w.Code)
		}
	}
	login := func() int {
		w := httptest.NewRecorder()
		a.login(w, httptest.NewRequest("POST", "/Users/AuthenticateByName", strings.NewReader(`{"Username":"viewer","Pw":"test-password-12345"}`)))
		return w.Code
	}
	if login() != 403 {
		t.Fatal("banned user can log in again")
	}
	w := httptest.NewRecorder()
	a.serve(w, httptest.NewRequest("POST", "/Sessions/Playing/Stopped?api_key=viewer-token", strings.NewReader(`{"ItemId":"movie"}`)))
	if w.Code != 204 {
		t.Fatal("banned user cannot release playback", w.Code, w.Body.String())
	}
	if _, err := a.db.Exec("INSERT INTO tokens(hash,user_id,device,expires) VALUES(?,?,?,?)", digest("logout-token"), "viewer", "cleanup", time.Now().Add(time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	a.serve(w, httptest.NewRequest("POST", "/Sessions/Logout?api_key=logout-token", nil))
	if w.Code != 204 {
		t.Fatal("banned user cannot log out", w.Code)
	}
	w = httptest.NewRecorder()
	a.antiTheftAPI(w, httptest.NewRequest("POST", "/admin/features/anti-theft/account", strings.NewReader(`{"UserID":"viewer"}`)))
	if w.Code != 200 || login() != 200 {
		t.Fatal("unban did not restore login")
	}
	w = httptest.NewRecorder()
	a.serve(w, httptest.NewRequest("GET", "/Users/Me?api_key=viewer-token", nil))
	if w.Code != 200 {
		t.Fatal("unban did not restore existing session")
	}
}

func TestAntiTheftIndependentSettingsAndAdminAuthorization(t *testing.T) {
	a, _, _, _ := cloudProtectionFixture(t)
	c := security.Defaults()
	c.ProtectCloudPlayback = false
	raw, _ := json.Marshal(c)
	w := httptest.NewRecorder()
	a.antiTheftAPI(w, httptest.NewRequest("PUT", "/admin/features/anti-theft", strings.NewReader(string(raw))))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	a.featurePlaybackAdmin(w, httptest.NewRequest("PUT", "/admin/features/playback", strings.NewReader(`{"Transcode":false,"ProtectCloudPlayback":true}`)))
	protected, err := a.cloudPlaybackProtection(context.Background())
	if w.Code != 200 || err != nil || protected {
		t.Fatal("playback settings overwrote independent protection")
	}
	w = httptest.NewRecorder()
	a.serve(w, httptest.NewRequest("GET", "/admin/features/anti-theft?api_key=viewer-token", nil))
	if w.Code != 403 {
		t.Fatal("ordinary user can manage protection")
	}
	bad := c
	bad.RequestsPerMinute = 0
	raw, _ = json.Marshal(bad)
	w = httptest.NewRecorder()
	a.antiTheftAPI(w, httptest.NewRequest("PUT", "/admin/features/anti-theft", strings.NewReader(string(raw))))
	if w.Code != 400 {
		t.Fatal("invalid settings accepted")
	}
	w = httptest.NewRecorder()
	a.antiTheftAPI(w, httptest.NewRequest("GET", "/admin/features/anti-theft", nil))
	var response struct {
		Settings security.Settings
		Accounts []M
		Events   []M
	}
	if json.Unmarshal(w.Body.Bytes(), &response) != nil || w.Code != 200 || response.Settings.ProtectCloudPlayback || len(response.Accounts) != 2 || response.Events == nil {
		t.Fatal("module response incorrect", w.Code)
	}
}
