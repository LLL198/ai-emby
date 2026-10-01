package main

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTargetedUpdatePreservesNeighbor(t *testing.T) {
	a := testApp(t)
	root := t.TempDir()
	one := filepath.Join(root, "one.strm")
	two := filepath.Join(root, "two.strm")
	for _, p := range []string{one, two} {
		os.WriteFile(p, []byte("https://example.com/old"), 0644)
	}
	a.db.Exec("INSERT INTO libraries(id,name,path,kind) VALUES('scope','日韩剧',?,'movies')", root)
	a.scanLibrary("scope")
	os.WriteFile(one, []byte("https://example.com/new"), 0644)
	os.Remove(two)
	a.scanLibraryScoped("scope", false, true, []string{one})
	var u string
	if e := a.db.QueryRow("SELECT url FROM items WHERE path=?", one).Scan(&u); e != nil || u != "https://example.com/new" {
		t.Fatal(u, e)
	}
	var n int
	a.db.QueryRow("SELECT count(*) FROM items WHERE path=?", two).Scan(&n)
	if n != 1 {
		t.Fatal("unrelated index changed")
	}
	a.scanLibraryScoped("scope", false, true, []string{two})
	a.db.QueryRow("SELECT count(*) FROM items WHERE lib='scope'").Scan(&n)
	if n != 1 {
		t.Fatal(n)
	}
}
func TestScanScheduleNext(t *testing.T) {
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	for _, c := range []scanSchedule{{Frequency: "minutes", Minutes: 1}, {Frequency: "daily", Time: "03:00"}, {Frequency: "weekly", Time: "03:00", Weekday: 1}} {
		n := nextScan(c, now)
		if !n.After(now) {
			t.Fatal(n)
		}
		if c.Frequency == "weekly" && n.Weekday() != time.Monday {
			t.Fatal(n)
		}
	}
}
func TestPlaybackProgressAndClear(t *testing.T) {
	a := testApp(t)
	u := User{ID: "u", Name: "Alice", Device: "TV"}
	a.logPlayback(httptest.NewRequest("GET", "/", nil), u, Item{ID: "film", Name: "Movie"})
	a.playbackProgress(u, "film", 50, 100)
	w := httptest.NewRecorder()
	a.activitySnapshot(w, httptest.NewRequest("GET", "/", nil))
	var b struct{ Entries []activityEntry }
	json.Unmarshal(w.Body.Bytes(), &b)
	if len(b.Entries) != 1 || b.Entries[0].Progress != 50 || !b.Entries[0].Online || b.Entries[0].UserID != "u" {
		t.Fatal(w.Body.String())
	}
	a.activitySnapshot(httptest.NewRecorder(), httptest.NewRequest("DELETE", "/", nil))
	w = httptest.NewRecorder()
	a.activitySnapshot(w, httptest.NewRequest("GET", "/", nil))
	json.Unmarshal(w.Body.Bytes(), &b)
	if len(b.Entries) != 0 {
		t.Fatal(w.Body.String())
	}
}
