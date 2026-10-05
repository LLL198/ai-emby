package security

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func fixture(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required")
	}
	root, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "security_" + strings.ReplaceAll(time.Now().Format("150405.000000000"), ".", "")
	if _, err = root.Exec("CREATE SCHEMA " + schema); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema+",public")
	u.RawQuery = q.Encode()
	db, err := sql.Open("postgres", u.String())
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE settings(k TEXT PRIMARY KEY,v TEXT NOT NULL);
	 CREATE TABLE users(id TEXT PRIMARY KEY,admin BIGINT NOT NULL DEFAULT 0);
	 CREATE TABLE plays(user_id TEXT,device TEXT);
	 INSERT INTO users VALUES('alice',0),('bob',0),('admin',1);` + Schema)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(); root.Exec("DROP SCHEMA " + schema + " CASCADE"); root.Close() })
	return db
}

func save(t *testing.T, db *sql.DB, c Settings) {
	t.Helper()
	raw, _ := json.Marshal(c)
	if _, err := db.Exec("INSERT INTO settings VALUES('feature:anti-theft',$1) ON CONFLICT(k) DO UPDATE SET v=excluded.v", string(raw)); err != nil {
		t.Fatal(err)
	}
}

func TestRollingAccountLimitAndWarning(t *testing.T) {
	db := fixture(t)
	ctx := context.Background()
	now := time.Unix(1800000000, 0)
	c := Defaults()
	c.RateLimitEnabled = true
	c.RequestsPerMinute = 2
	save(t, db, c)
	check := func(user string, count bool, at time.Time) Decision {
		d, err := Admit(ctx, db, user, count, at)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	for i := 0; i < 20; i++ {
		if check("alice", false, now).Warning {
			t.Fatal("metadata counted")
		}
	}
	if check("alice", true, now).Warning || check("alice", true, now.Add(time.Second)).Warning {
		t.Fatal("limit boundary rejected")
	}
	if d := check("alice", true, now.Add(2*time.Second)); !d.Warning || d.Banned(now) {
		t.Fatal("warning must allow playback")
	}
	for i := 0; i < 30; i++ {
		check("alice", true, now.Add(3*time.Second))
	}
	var events, stored int
	db.QueryRow("SELECT count(*) FROM anti_theft_events").Scan(&events)
	db.QueryRow("SELECT cardinality(requests) FROM anti_theft_accounts WHERE user_id='alice'").Scan(&stored)
	if events != 1 || stored != 3 {
		t.Fatalf("warning flood/storage: %d %d", events, stored)
	}
	if check("bob", true, now).Warning {
		t.Fatal("accounts must be independent")
	}
	if !check("alice", true, now.Add(61*time.Second)).Warning {
		t.Fatal("fixed minute reset bypassed rolling window")
	}
	if check("alice", true, now.Add(122*time.Second)).Warning {
		t.Fatal("expired requests retained")
	}
}

func TestConcurrentBanAndRecovery(t *testing.T) {
	for _, action := range []string{"temporary", "permanent"} {
		t.Run(action, func(t *testing.T) {
			db := fixture(t)
			ctx := context.Background()
			now := time.Unix(1800000000, 0)
			c := Defaults()
			c.RateLimitEnabled = true
			c.RequestsPerMinute = 2
			c.Action = action
			c.TemporaryBanMinutes = 1
			save(t, db, c)
			db.Exec("INSERT INTO plays VALUES('alice','device')")
			var allowed atomic.Int32
			var wg sync.WaitGroup
			for i := 0; i < 24; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					d, err := Admit(ctx, db, "alice", true, now)
					if err != nil {
						t.Error(err)
						return
					}
					if !d.Banned(now) {
						allowed.Add(1)
					}
				}()
			}
			wg.Wait()
			if allowed.Load() != 2 {
				t.Fatalf("concurrent limit bypass: %d", allowed.Load())
			}
			var events, plays int
			db.QueryRow("SELECT count(*) FROM anti_theft_events").Scan(&events)
			db.QueryRow("SELECT count(*) FROM plays").Scan(&plays)
			if events != 1 || plays != 0 {
				t.Fatal("ban not atomic or playback slot retained")
			}
			ban, err := Ban(ctx, db, "alice")
			if err != nil || !ban.Banned(now) {
				t.Fatal("persistent ban missing", err)
			}
			c.RateLimitEnabled = false
			save(t, db, c)
			if d, err := Admit(ctx, db, "alice", false, now); err != nil || !d.Banned(now) {
				t.Fatal("disabled counting bypassed existing ban")
			}
			if action == "temporary" && ban.Banned(now.Add(time.Minute)) {
				t.Fatal("temporary ban did not expire at boundary")
			}
			if action == "permanent" && !ban.Banned(now.Add(365*24*time.Hour)) {
				t.Fatal("permanent ban expired")
			}
			if err = UpdateAccount(ctx, db, "alice", nil, true, now); err != nil {
				t.Fatal(err)
			}
			ban, err = Ban(ctx, db, "alice")
			if err != nil || ban.Banned(now) {
				t.Fatal("manual recovery failed")
			}
			c.RateLimitEnabled = true
			save(t, db, c)
			if d, err := Admit(ctx, db, "alice", true, now); err != nil || d.Banned(now) {
				t.Fatal("unban left stale counter")
			}
		})
	}
}

func TestOverridesAndAdministratorExemption(t *testing.T) {
	db := fixture(t)
	ctx := context.Background()
	now := time.Unix(1800000000, 0)
	c := Defaults()
	c.RateLimitEnabled = true
	c.RequestsPerMinute = 1
	c.Action = "permanent"
	save(t, db, c)
	limit := 0
	if err := UpdateAccount(ctx, db, "alice", &limit, false, now); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		for _, user := range []string{"alice", "admin"} {
			d, err := Admit(ctx, db, user, true, now)
			if err != nil || d.Banned(now) {
				t.Fatal("exempt account banned", user, err)
			}
		}
	}
	if err := UpdateAccount(ctx, db, "alice", nil, false, now); err != nil {
		t.Fatal(err)
	}
	Admit(ctx, db, "alice", true, now)
	if d, err := Admit(ctx, db, "alice", true, now); err != nil || !d.Permanent {
		t.Fatal("global limit not restored")
	}
	if err := UpdateAccount(ctx, db, "admin", &limit, false, now); err == nil {
		t.Fatal("admin override should be rejected")
	}
}

func TestSettingsMigrationAndValidation(t *testing.T) {
	db := fixture(t)
	ctx := context.Background()
	c, err := Load(ctx, db)
	if err != nil || !c.ProtectCloudPlayback || c.RateLimitEnabled {
		t.Fatal("wrong defaults")
	}
	db.Exec(`INSERT INTO settings VALUES('feature:playback','{"ProtectCloudPlayback":false}')`)
	c, err = Load(ctx, db)
	if err != nil || c.ProtectCloudPlayback {
		t.Fatal("legacy opt-out lost")
	}
	c.ProtectCloudPlayback = true
	save(t, db, c)
	c, err = Load(ctx, db)
	if err != nil || !c.ProtectCloudPlayback {
		t.Fatal("module settings not authoritative")
	}
	db.Exec(`UPDATE settings SET v='invalid' WHERE k='feature:anti-theft'`)
	if _, err = Load(ctx, db); err == nil {
		t.Fatal("corrupt settings accepted")
	}
	c = Defaults()
	c.Action = "unknown"
	if Validate(c) == nil {
		t.Fatal("invalid action accepted")
	}
	c = Defaults()
	c.RequestsPerMinute = 0
	if Validate(c) == nil {
		t.Fatal("invalid limit accepted")
	}
}

func TestPlaybackScopeAndTokenFormats(t *testing.T) {
	for _, path := range []string{"/Videos/i/stream.mkv", "/Videos/i/streamlegacy", "/emby/emby/Videos/i/original", "/emby/Items/i/Playback/stream.mp4", "/features/stream/i", "/features/stream/i/video.mp4", "/Audio/i/stream"} {
		for _, method := range []string{"GET", "HEAD"} {
			if !PlaybackEntry(httptest.NewRequest(method, path, nil)) {
				t.Fatal("playback missed", method, path)
			}
		}
	}
	for _, path := range []string{"/Items/i/Images/Primary", "/Items/i/PlaybackInfo", "/Sessions/Playing/Progress", "/Videos/i/Subtitles/0/Stream.srt", "/Library/Refresh", "/cloud/resolve/mount", "/admin/features/anti-theft"} {
		if PlaybackEntry(httptest.NewRequest("GET", path, nil)) {
			t.Fatal("non-playback counted", path)
		}
	}
	if PlaybackEntry(httptest.NewRequest("POST", "/Videos/i/stream", nil)) {
		t.Fatal("unsupported method counted")
	}
	for _, tc := range []struct{ path, header, value string }{
		{"/?API_KEY=viewer", "", ""}, {"/?X-MediaBrowser-Token=viewer", "", ""},
		{"/?X-Emby-Authorization=Emby%20Token%3D%22viewer%22", "", ""},
		{"/", "Authorization", `Emby Client="Filmly", Token="viewer"`}, {"/", "Authorization", "Bearer viewer"},
		{"/", "X-Emby-Token", "viewer"}, {"/", "X-Emby-Api-Key", "viewer"},
	} {
		r := httptest.NewRequest("GET", tc.path, nil)
		if tc.header != "" {
			r.Header.Set(tc.header, tc.value)
		}
		if Token(r) != "viewer" {
			t.Fatal("authentication format could bypass rate accounting", tc)
		}
	}
}
