package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/LLL198/ai-emby/security"
)

func antiTheftGatewayDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required")
	}
	root, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "gateway_security_" + strconv.FormatInt(time.Now().UnixNano(), 10)
	if _, err = root.Exec("CREATE SCHEMA " + schema); err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(dsn)
	q := u.Query()
	q.Set("search_path", schema+",public")
	u.RawQuery = q.Encode()
	db, err := sql.Open("postgres", u.String())
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE settings(k TEXT PRIMARY KEY,v TEXT NOT NULL);
	 CREATE TABLE users(id TEXT PRIMARY KEY,admin BIGINT NOT NULL DEFAULT 0,can_view_hidden_libraries BIGINT NOT NULL DEFAULT 0);
	 CREATE TABLE tokens(hash TEXT PRIMARY KEY,user_id TEXT,device TEXT,expires BIGINT);
	 CREATE TABLE api_keys(hash TEXT);
	 CREATE TABLE plays(user_id TEXT,device TEXT);
	 INSERT INTO users VALUES('viewer',0,0),('admin',1,0);` + security.Schema)
	if err != nil {
		t.Fatal(err)
	}
	for _, user := range []string{"viewer", "admin"} {
		digest := sha256.Sum256([]byte(user + "-token"))
		if _, err = db.Exec("INSERT INTO tokens VALUES($1,$2,'device',$3)", hex.EncodeToString(digest[:]), user, time.Now().Add(time.Hour).Unix()); err != nil {
			t.Fatal(err)
		}
	}
	c := security.Defaults()
	c.RateLimitEnabled = true
	c.RequestsPerMinute = 1
	c.Action = "temporary"
	raw, _ := json.Marshal(c)
	db.Exec("INSERT INTO settings VALUES('feature:anti-theft',$1)", string(raw))
	t.Cleanup(func() { db.Close(); root.Exec("DROP SCHEMA " + schema + " CASCADE"); root.Close() })
	return db
}

func TestGatewayAntiTheftCountsExternalPlaybackOnce(t *testing.T) {
	db := antiTheftGatewayDB(t)
	request := func(path, method, token string) (bool, *httptest.ResponseRecorder) {
		r := httptest.NewRequest(method, path, nil)
		r.Header.Set("X-Emby-Token", token)
		w := httptest.NewRecorder()
		blocked := enforceAntiTheft(db, w, r)
		return blocked, w
	}
	for _, path := range []string{"/Items/i/PlaybackInfo", "/Items/i/Images/Primary", "/Sessions/Playing/Progress", "/admin/features/anti-theft"} {
		if blocked, _ := request(path, "GET", "viewer-token"); blocked {
			t.Fatal("metadata counted", path)
		}
	}
	if blocked, _ := request("/Videos/i/stream.mkv", "GET", "viewer-token"); blocked {
		t.Fatal("first playback blocked")
	}
	blocked, w := request("/emby/Videos/i/stream.mkv", "HEAD", "viewer-token")
	if !blocked || w.Code != 403 || w.Header().Get("Retry-After") == "" {
		t.Fatal("HEAD bypassed shared account limit", w.Code)
	}
	if blocked, w = request("/Items/i", "GET", "viewer-token"); !blocked || w.Code != 403 {
		t.Fatal("ban did not block account access")
	}
	for i := 0; i < 5; i++ {
		if blocked, _ := request("/Videos/i/stream", "GET", "admin-token"); blocked {
			t.Fatal("admin locked out")
		}
	}
	for _, path := range []string{"/Sessions/Logout", "/emby/Sessions/Playing/Stopped"} {
		if blocked, _ := request(path, "POST", "viewer-token"); blocked {
			t.Fatal("cleanup blocked")
		}
	}
	if blocked, _ := request("/Videos/i/stream", "OPTIONS", "viewer-token"); blocked {
		t.Fatal("CORS blocked")
	}
	if blocked, _ := request("/Videos/i/stream", "GET", ""); blocked {
		t.Fatal("gateway must leave anonymous authentication to core")
	}
	var events int
	db.QueryRow("SELECT count(*) FROM anti_theft_events").Scan(&events)
	if events != 1 {
		t.Fatal("duplicate enforcement/event", events)
	}
}

func TestGatewayWarningPreservesRedirectResponse(t *testing.T) {
	db := antiTheftGatewayDB(t)
	db.Exec(`UPDATE settings SET v=jsonb_set(v::jsonb,'{Action}','"warn"')::text WHERE k='feature:anti-theft'`)
	for i := 0; i < 2; i++ {
		r := httptest.NewRequest("GET", "/Videos/i/stream?API_KEY=viewer-token", nil)
		w := httptest.NewRecorder()
		if enforceAntiTheft(db, w, r) {
			t.Fatal("warning blocked playback")
		}
		w.Header().Set("Location", "https://cdn.example/movie.mkv")
		w.WriteHeader(302)
		if w.Code != 302 || (i == 1 && w.Header().Get("X-AI-Emby-Protection-Warning") == "") {
			t.Fatal("redirect or warning header lost")
		}
	}
}
