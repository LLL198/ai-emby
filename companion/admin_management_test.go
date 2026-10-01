package main

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWatchDelayAndIdentity(t *testing.T) {
	a := testApp(t)
	if a.watchDelay() != 30 {
		t.Fatal("default")
	}
	w := httptest.NewRecorder()
	a.enhancementSettings(w, httptest.NewRequest("PUT", "/admin/enhancements", strings.NewReader(`{"WatchDelaySeconds":9}`)))
	if w.Code != 400 {
		t.Fatal(w.Code)
	}
	w = httptest.NewRecorder()
	a.enhancementSettings(w, httptest.NewRequest("PUT", "/admin/enhancements", strings.NewReader(`{"WatchDelaySeconds":10}`)))
	if w.Code != 200 || a.watchDelay() != 10 {
		t.Fatal(w.Code)
	}
	var uid string
	a.db.QueryRow("SELECT id FROM users LIMIT 1").Scan(&uid)
	w = httptest.NewRecorder()
	a.userIdentity(w, httptest.NewRequest("PUT", "/admin/user-identity", strings.NewReader(`{"ID":"`+uid+`","Name":"renamed","Password":"new-password-123"}`)))
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var name string
	a.db.QueryRow("SELECT name FROM users WHERE id=?", uid).Scan(&name)
	if name != "renamed" {
		t.Fatal(name)
	}
}
func TestWatcherDebounce(t *testing.T) {
	a := testApp(t)
	root := t.TempDir()
	a.db.Exec("INSERT INTO libraries(id,name,path,kind) VALUES('watch','watch',?,'movies')", root)
	a.db.Exec("INSERT INTO settings VALUES('watch_delay_seconds','10')")
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan struct{})
	go func() { defer close(finished); a.watchMediaContext(ctx) }()
	defer func() { cancel(); <-finished; a.scan.Lock(); a.scan.Unlock() }()
	time.Sleep(time.Second)
	p := filepath.Join(root, "movie.strm")
	os.WriteFile(p, []byte("https://example.com/video"), 0644)
	time.Sleep(2 * time.Second)
	os.WriteFile(p, []byte("https://example.com/video2"), 0644)
	time.Sleep(3 * time.Second)
	var n int
	a.db.QueryRow("SELECT count(*) FROM items WHERE lib='watch'").Scan(&n)
	if n != 0 {
		t.Fatal("scanned before debounce")
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		a.db.QueryRow("SELECT count(*) FROM items WHERE lib='watch'").Scan(&n)
		if n == 1 {
			break
		}
		time.Sleep(time.Second)
	}
	if n != 1 {
		t.Fatal("watcher did not scan")
	}
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		a.db.QueryRow("SELECT count(*) FROM items WHERE lib='watch'").Scan(&n)
		if n == 0 {
			return
		}
		time.Sleep(time.Second)
	}
	t.Fatal("watcher did not remove last deleted file")
}
