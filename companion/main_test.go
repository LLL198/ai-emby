package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testApp(t *testing.T) *App {
	t.Helper()
	t.Setenv("ADMIN_PASSWORD", "test-password-12345")
	t.Setenv("BOOTSTRAP_API_KEY", "")
	t.Setenv("MEDIA_INFO_ROOT", t.TempDir())
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required for PostgreSQL integration tests")
	}
	base, e := openDatabase(dsn)
	if e != nil {
		t.Fatal(e)
	}
	schema := "test_" + id()
	if _, e = base.Exec("CREATE SCHEMA " + schema); e != nil {
		t.Fatal(e)
	}
	testDSN := dsn + " search_path=" + schema + ",public"
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		u, err := url.Parse(dsn)
		if err != nil {
			t.Fatal(err)
		}
		q := u.Query()
		q.Set("search_path", schema+",public")
		u.RawQuery = q.Encode()
		testDSN = u.String()
	}
	db, e := openDatabase(testDSN)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { db.Close(); base.Exec(fmt.Sprintf("DROP SCHEMA %s CASCADE", schema)); base.Close() })
	a := &App{db: db, attempts: map[string][]time.Time{}}
	a.init()
	t.Cleanup(func() { db.Close() })
	return a
}
func TestDeviceLimitConcurrent(t *testing.T) {
	a := testApp(t)
	var uid string
	if e := a.db.QueryRow("SELECT id FROM users LIMIT 1").Scan(&uid); e != nil {
		t.Fatal(e)
	}
	var ok atomic.Int32
	var wg sync.WaitGroup
	for n := 0; n < 20; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if a.reserve(User{ID: uid, Device: id(), Max: 2}, "movie") == nil {
				ok.Add(1)
			}
		}()
	}
	wg.Wait()
	if ok.Load() != 2 {
		t.Fatalf("allowed %d devices, want 2", ok.Load())
	}
}
func TestStreamRedirectDoesNotFetchSource(t *testing.T) {
	a := testApp(t)
	var fetched atomic.Int32
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fetched.Add(1) }))
	defer source.Close()
	_, e := a.db.Exec("INSERT INTO libraries(id,name,path,kind) VALUES('l','test','/media/test','movies')")
	if e != nil {
		t.Fatal(e)
	}
	_, e = a.db.Exec("INSERT INTO items(id,lib,parent,name,kind,path,url,seen) VALUES('i','l','l','movie','Movie','/media/test/a.strm',?,'g')", source.URL)
	if e != nil {
		t.Fatal(e)
	}
	w := httptest.NewRecorder()
	a.stream(w, httptest.NewRequest("GET", "/Videos/i/stream", nil), User{API: true}, "i")
	if w.Code != 302 || w.Header().Get("Location") != source.URL || w.Body.Len() != 0 || fetched.Load() != 0 {
		t.Fatalf("unexpected stream behavior: %d %d %d", w.Code, w.Body.Len(), fetched.Load())
	}
}

func TestGatewayRejectsVideoBodyAndUnauthenticatedRequests(t *testing.T) {
	a := testApp(t)
	var requests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "video/mp4")
		w.Write([]byte("must-not-relay-video"))
	}))
	defer upstream.Close()
	_, e := a.db.Exec("INSERT INTO api_keys VALUES('test','test',?,0)", digest("test-key"))
	if e != nil {
		t.Fatal(e)
	}
	h := a.gatewayFor(upstream.URL)
	unauthorized := httptest.NewRecorder()
	h.ServeHTTP(unauthorized, httptest.NewRequest("GET", "/emby/Videos/test/stream.mkv", nil))
	if unauthorized.Code != 401 || requests.Load() != 0 {
		t.Fatal("unauthenticated request reached upstream")
	}
	r := httptest.NewRequest("GET", "/emby/Videos/test/stream.mkv", nil)
	r.Header.Set("X-Emby-Token", "test-key")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 502 || strings.Contains(w.Body.String(), "must-not-relay-video") {
		t.Fatalf("video fallback was not blocked: %d", w.Code)
	}
}
