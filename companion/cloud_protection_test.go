package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/LLL198/ai-emby/security"
)

type cloudProtectionTransport func(*http.Request) (*http.Response, error)

func (f cloudProtectionTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestCloudInternalCredentialScope(t *testing.T) {
	m := cloudMount{ID: "mount", Secret: "private-test-mount-secret"}
	now := time.Unix(1800000000, 0)
	p := "/movie.mkv"
	expiry := strconv.FormatInt(now.Add(time.Hour).Unix(), 10)
	base := "/cloud/resolve/mount?" + url.Values{
		"internal_expires": {expiry}, "internal_token": {cloudInternalSignature(m, p, expiry)},
	}.Encode()
	for _, tc := range []struct {
		name, target, remote, file string
		mount                      cloudMount
		at                         time.Time
		allowed                    bool
	}{
		{"valid", base, "127.0.0.1:3210", p, m, now, true},
		{"ipv6", base, "[::1]:3210", p, m, now, true},
		{"remote client", base, "192.0.2.10:3210", p, m, now, false},
		{"different file", base, "127.0.0.1:3210", "/other.mkv", m, now, false},
		{"different mount", base, "127.0.0.1:3210", p, cloudMount{ID: "other", Secret: m.Secret}, now, false},
		{"rotated secret", base, "127.0.0.1:3210", p, cloudMount{ID: m.ID, Secret: "other"}, now, false},
		{"expired", base, "127.0.0.1:3210", p, m, now.Add(time.Hour), false},
		{"duplicate credential", base + "&internal_token=forged", "127.0.0.1:3210", p, m, now, false},
		{"duplicate expiry", base + "&internal_expires=" + expiry, "127.0.0.1:3210", p, m, now, false},
		{"forged", "/?internal_token=00&internal_expires=" + expiry, "127.0.0.1:3210", p, m, now, false},
		{"permanent source sign", "/?internal_token=" + cloudSign(m, p) + "&internal_expires=" + expiry, "127.0.0.1:3210", p, m, now, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", tc.target, nil)
			r.RemoteAddr = tc.remote
			r.Header.Set("X-Forwarded-For", "127.0.0.1")
			if validCloudInternalRequest(r, tc.mount, tc.file, tc.at) != tc.allowed {
				t.Fatal("unexpected internal task authorization")
			}
		})
	}
}

func cloudProtectionFixture(t *testing.T) (*App, cloudMount, string, *int) {
	t.Helper()
	a := testApp(t)
	if _, err := a.db.DB.Exec(featureSchema); err != nil {
		t.Fatal(err)
	}
	m := cloudMount{ID: "mount", Name: "test", Driver: "WebDav", Secret: "test-mount-secret", Enabled: true, PlaybackMode: "proxy"}
	queries := []struct {
		sql  string
		args []any
	}{
		{"INSERT INTO feature_cloud_mounts(id,name,driver,secret,enabled,created,playback_mode) VALUES(?,?,?,?,1,0,?)", []any{m.ID, m.Name, m.Driver, m.Secret, m.PlaybackMode}},
		{"INSERT INTO users(id,name,hash,max_devices) VALUES('viewer','viewer','unused',1)", nil},
		{"INSERT INTO tokens(hash,user_id,device,expires) VALUES(?,'viewer','device',?)", []any{digest("viewer-token"), time.Now().Add(time.Hour).Unix()}},
		{"INSERT INTO libraries(id,name,path,kind) VALUES('lib','library','/media/protected','movies')", nil},
	}
	for _, q := range queries {
		if _, err := a.db.Exec(q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}
	source := "https://panel.example/cloud/resolve/mount?" + url.Values{"path": {"/movie.mkv"}, "sign": {cloudSign(m, "/movie.mkv")}}.Encode()
	if _, err := a.db.Exec("INSERT INTO items(id,lib,parent,name,kind,path,url,seen) VALUES('movie','lib','lib','movie','Movie','/media/protected/movie.strm',?,'g')", source); err != nil {
		t.Fatal(err)
	}
	oldHTTP, oldProxy := cloudHTTP, cloudProxyHTTP
	cloudClient.Lock()
	oldToken, oldExpiry := cloudClient.token, cloudClient.expires
	cloudClient.token, cloudClient.expires = "engine-test-token", time.Now().Add(time.Hour)
	cloudClient.Unlock()
	calls := new(int)
	cloudHTTP = &http.Client{Transport: cloudProtectionTransport(func(r *http.Request) (*http.Response, error) {
		*calls++
		if r.Header.Get("Authorization") != "engine-test-token" {
			t.Error("viewer credential reached the cloud engine")
		}
		data := `{"code":200,"data":{"sign":"engine-sign","is_dir":false}}`
		if r.URL.Path == "/api/fs/link" {
			data = `{"code":200,"data":{"url":"https://cdn.example/movie.mkv?provider-sign=temporary"}}`
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(data)), Request: r}, nil
	})}
	cloudProxyHTTP = &http.Client{Transport: cloudProtectionTransport(func(r *http.Request) (*http.Response, error) {
		*calls++
		if r.URL.Host != "127.0.0.1:18099" || r.Header.Get("X-Emby-Token") != "" {
			t.Error("proxy origin or credential isolation changed")
		}
		h := http.Header{"Content-Type": {"video/mp4"}, "Accept-Ranges": {"bytes"}, "Content-Length": {"5"}}
		status, data := 200, "video"
		switch r.Header.Get("Range") {
		case "bytes=0-0":
			status, data = 206, "v"
			h.Set("Content-Length", "1")
			h.Set("Content-Range", "bytes 0-0/5")
		case "bytes=1-2":
			status, data = 206, "id"
			h.Set("Content-Length", "2")
			h.Set("Content-Range", "bytes 1-2/5")
		}
		return &http.Response{StatusCode: status, Header: h, Body: io.NopCloser(strings.NewReader(data)), Request: r}, nil
	})}
	t.Cleanup(func() {
		cloudHTTP, cloudProxyHTTP = oldHTTP, oldProxy
		cloudClient.Lock()
		cloudClient.token, cloudClient.expires = oldToken, oldExpiry
		cloudClient.Unlock()
	})
	return a, m, source, calls
}

func setCloudProtection(t *testing.T, a *App, enabled bool) {
	t.Helper()
	c := security.Defaults()
	c.ProtectCloudPlayback = enabled
	data, _ := json.Marshal(c)
	w := httptest.NewRecorder()
	a.antiTheftAPI(w, httptest.NewRequest("PUT", "/admin/features/anti-theft", strings.NewReader(string(data))))
	if w.Code != 200 {
		t.Fatalf("save protection setting: %d %s", w.Code, w.Body.String())
	}
	// Simulate another process reading the shared setting, with no cached state.
	other := &App{db: a.db}
	value, err := other.cloudPlaybackProtection(context.Background())
	if err != nil || value != enabled {
		t.Fatalf("protection setting did not persist: %v %v", value, err)
	}
}

func TestCloudProtectionSwitchAndPlayback(t *testing.T) {
	a, _, source, calls := cloudProtectionFixture(t)
	bare := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		a.cloudResolve(w, httptest.NewRequest("GET", source, nil))
		return w
	}
	if w := bare(); w.Code != 401 || *calls != 0 {
		t.Fatalf("default protection did not reject bare STRM playback: %d %s", w.Code, w.Body.String())
	}
	setCloudProtection(t, a, true)
	before := *calls
	if w := bare(); w.Code != 401 || *calls != before || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("permanent STRM credential bypassed protection or reached provider")
	}
	// Public headers and a user token do not impersonate an in-process request.
	r := httptest.NewRequest("GET", source, nil)
	r.Header.Set("X-Emby-Token", "viewer-token")
	r.Header.Set("X-Go-Emby-Internal", "true")
	r.RemoteAddr = "127.0.0.1:12345"
	w := httptest.NewRecorder()
	a.cloudResolve(w, r)
	if w.Code != 401 || *calls != before {
		t.Fatal("HTTP headers granted internal playback authority")
	}
	for _, tc := range []struct {
		method, path, byteRange, body string
		status                        int
	}{
		{"GET", "/Videos/movie/stream.mkv", "", "video", 200},
		{"GET", "/Items/movie/Playback/stream.mkv", "bytes=1-2", "id", 206},
		{"HEAD", "/emby/Videos/movie/stream.mkv", "", "", 200},
	} {
		r := httptest.NewRequest(tc.method, tc.path+"?api_key=viewer-token", nil)
		r.Header.Set("Range", tc.byteRange)
		w := httptest.NewRecorder()
		a.serve(w, r)
		if w.Code != tc.status || w.Body.String() != tc.body || w.Header().Get("Location") != "" {
			t.Fatalf("protected playback %s: %d %s %s", tc.method, w.Code, w.Body.String(), w.Header().Get("Location"))
		}
		for _, entry := range a.activity.entries {
			if entry.Category == "redirect" && entry.State == "error" {
				t.Fatal("successful protected proxy playback logged as an error")
			}
		}
	}
	setCloudProtection(t, a, false)
	if w := bare(); w.Code != 200 || w.Body.String() != "video" {
		t.Fatalf("disabling protection did not restore the existing STRM: %d", w.Code)
	}
}

func TestCloudProtectionDefaultsAndExplicitOverride(t *testing.T) {
	a, _, source, _ := cloudProtectionFixture(t)
	for _, tc := range []struct {
		name, setting string
		protected     bool
	}{
		{"new installation", "", true},
		{"legacy playback settings", `{"Transcode":true}`, true},
		{"saved off", `{"ProtectCloudPlayback":false}`, false},
		{"saved on", `{"ProtectCloudPlayback":true}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := a.db.Exec("DELETE FROM settings WHERE k IN ('feature:playback','feature:anti-theft')"); err != nil {
				t.Fatal(err)
			}
			if tc.setting != "" {
				if _, err := a.db.Exec("INSERT INTO settings(k,v) VALUES('feature:playback',?)", tc.setting); err != nil {
					t.Fatal(err)
				}
			}
			protected, err := a.cloudPlaybackProtection(context.Background())
			if err != nil || protected != tc.protected {
				t.Fatalf("playback enforcement and settings defaults differ: %v %v", protected, err)
			}
			w := httptest.NewRecorder()
			a.cloudResolve(w, httptest.NewRequest("GET", source, nil))
			want := http.StatusOK
			if tc.protected {
				want = http.StatusUnauthorized
			}
			if w.Code != want {
				t.Fatalf("bare STRM playback: %d, want %d", w.Code, want)
			}
			// Updating another setting must preserve a saved explicit opt-out.
			w = httptest.NewRecorder()
			a.featurePlaybackAdmin(w, httptest.NewRequest("PUT", "/admin/features/playback", strings.NewReader(`{"Transcode":false}`)))
			protected, err = a.cloudPlaybackProtection(context.Background())
			if w.Code != 200 || err != nil || protected != tc.protected {
				t.Fatalf("partial settings update changed protection: %d %v %v", w.Code, protected, err)
			}
		})
	}
}

func TestCloudPlaybackRejectsConflictingCredentials(t *testing.T) {
	a, _, _, calls := cloudProtectionFixture(t)
	for _, query := range []string{
		"api_key=invalid&API_KEY=viewer-token",
		"API_KEY=viewer-token&api_key=invalid",
		"api_key=viewer-token&api_key=invalid",
		"X-Emby-Token=viewer-token&x-emby-token=invalid",
	} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			r := httptest.NewRequest(method, "/Videos/movie/stream.mkv?"+query, nil)
			r.Header.Set("X-Emby-Token", "viewer-token")
			w := httptest.NewRecorder()
			a.serve(w, r)
			if w.Code != http.StatusUnauthorized || *calls != 0 || w.Header().Get("Location") != "" {
				t.Fatalf("conflicting playback credentials reached a media source: HTTP %d", w.Code)
			}
		}
	}
}

func TestCloudProtectionRejectsRevokedAndRestrictedPlayback(t *testing.T) {
	a, _, _, calls := cloudProtectionFixture(t)
	setCloudProtection(t, a, true)
	request := func() int {
		before := *calls
		w := httptest.NewRecorder()
		a.serve(w, httptest.NewRequest("GET", "/Videos/movie/stream.mkv?api_key=viewer-token", nil))
		if *calls != before {
			t.Fatal("denied playback reached the provider")
		}
		return w.Code
	}
	if _, err := a.db.Exec("INSERT INTO user_playback(user_id,allowed) VALUES('viewer',0)"); err != nil {
		t.Fatal(err)
	}
	if status := request(); status != 403 {
		t.Fatalf("disabled playback: %d", status)
	}
	a.db.Exec("DELETE FROM user_playback WHERE user_id='viewer'")
	if _, err := a.db.Exec("INSERT INTO plays VALUES('viewer','other-device','movie',?)", time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	if status := request(); status != 403 {
		t.Fatalf("device limit: %d", status)
	}
	a.db.Exec("DELETE FROM plays WHERE user_id='viewer'")
	if _, err := a.db.Exec("INSERT INTO feature_library_policy(lib,data) VALUES('lib',?)", `{"RestrictUsers":true,"Users":[]}`); err != nil {
		t.Fatal(err)
	}
	if status := request(); status != 404 {
		t.Fatalf("library restriction: %d", status)
	}
	a.db.Exec("DELETE FROM feature_library_policy WHERE lib='lib'")
	if _, err := a.db.Exec("DELETE FROM tokens WHERE user_id='viewer'"); err != nil {
		t.Fatal(err)
	}
	if status := request(); status != 401 {
		t.Fatalf("revoked session: %d", status)
	}
}

func TestCloudProtectionInternalTasksAndRedirect(t *testing.T) {
	a, m, source, calls := cloudProtectionFixture(t)
	setCloudProtection(t, a, true)
	input, err := a.featureMediaInput(Item{Path: "/media/movie.strm", URL: source})
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(input)
	if u.Host != "127.0.0.1:18098" || u.Query().Get("internal_token") == "" {
		t.Fatal("media task did not receive a private scoped source")
	}
	r := httptest.NewRequest("GET", input, nil)
	r.RemoteAddr = "127.0.0.1:3210"
	w := httptest.NewRecorder()
	a.cloudResolve(w, r)
	if w.Code != 200 || w.Body.String() != "video" {
		t.Fatalf("internal source: %d %s", w.Code, w.Body.String())
	}
	before := *calls
	u.RawQuery += "&path=%2Fother.mkv"
	w = httptest.NewRecorder()
	r = httptest.NewRequest("GET", u.String(), nil)
	r.RemoteAddr = "192.0.2.1:3210"
	a.cloudResolve(w, r)
	if w.Code < 400 || *calls != before {
		t.Fatal("modified internal credential reached provider")
	}
	// Switch the same source to redirect mode without regenerating the STRM.
	if _, err := a.db.Exec("UPDATE feature_cloud_mounts SET driver='115 Cloud',playback_mode='redirect' WHERE id=?", m.ID); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	a.serve(w, httptest.NewRequest("GET", "/Videos/movie/stream.mkv?api_key=viewer-token", nil))
	if w.Code != 302 || w.Header().Get("Location") != "https://cdn.example/movie.mkv?provider-sign=temporary" || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("protected redirect: %d %s", w.Code, w.Header().Get("Location"))
	}
	if strings.Contains(w.Header().Get("Location"), "internal_token") || strings.Contains(w.Header().Get("Location"), "viewer-token") {
		t.Fatal("server credentials leaked")
	}
}

func TestCloudProtectionFailsClosed(t *testing.T) {
	a, _, source, calls := cloudProtectionFixture(t)
	if _, err := a.db.Exec("INSERT INTO settings VALUES('feature:playback','invalid-json')"); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	a.cloudResolve(w, httptest.NewRequest("GET", source, nil))
	if w.Code != 503 || *calls != 0 {
		t.Fatal("invalid protection setting permitted playback")
	}
	if _, err := a.cloudInternalInput(source); err == nil {
		t.Fatal("invalid setting permitted an internal task source")
	}
	if err := a.db.Close(); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	a.cloudResolve(w, httptest.NewRequest("GET", source, nil))
	if w.Code != 503 || *calls != 0 {
		t.Fatal("database outage disabled protection")
	}
}

func TestCloudProtectionProbeUsesPrivateSource(t *testing.T) {
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe required for real media-probe integration")
	}
	a, m, _, _ := cloudProtectionFixture(t)
	setCloudProtection(t, a, true)
	x, err := a.item("movie")
	if err != nil {
		t.Fatal(err)
	}
	// One second of mono PCM audio is enough for an actual FFprobe run.
	var audio bytes.Buffer
	audio.WriteString("RIFF")
	binary.Write(&audio, binary.LittleEndian, uint32(36+16000))
	audio.WriteString("WAVEfmt ")
	binary.Write(&audio, binary.LittleEndian, uint32(16))
	binary.Write(&audio, binary.LittleEndian, uint16(1))
	binary.Write(&audio, binary.LittleEndian, uint16(1))
	binary.Write(&audio, binary.LittleEndian, uint32(8000))
	binary.Write(&audio, binary.LittleEndian, uint32(16000))
	binary.Write(&audio, binary.LittleEndian, uint16(2))
	binary.Write(&audio, binary.LittleEndian, uint16(16))
	audio.WriteString("data")
	binary.Write(&audio, binary.LittleEndian, uint32(16000))
	audio.Write(make([]byte, 16000))
	requests := 0
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !validCloudInternalRequest(r, m, "/movie.mkv", time.Now()) || token(r) != "" {
			t.Error("media probe did not use independent private task authority")
			w.WriteHeader(401)
			return
		}
		requests++
		http.ServeContent(w, r, "sample.wav", time.Time{}, bytes.NewReader(audio.Bytes()))
	}))
	server.Listener, err = net.Listen("tcp", "127.0.0.1:18098")
	if err != nil {
		t.Fatal(err)
	}
	server.Start()
	defer server.Close()
	for _, viewerToken := range []string{"", "revoked-user-token", "background-api-key"} {
		data, err := a.extractMedia(context.Background(), x, viewerToken, "test-probe")
		if err != nil || data["RunTimeTicks"] != int64(1e7) {
			t.Fatalf("real probe failed with viewer token %q: %v %v", viewerToken, data, err)
		}
	}
	if requests < 3 {
		t.Fatal("probe never fetched the private source")
	}
}
