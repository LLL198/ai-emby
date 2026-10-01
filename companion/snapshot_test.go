package main

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSnapshotDiffAndSeasonPosters(t *testing.T) {
	a := testApp(t)
	os.MkdirAll("/media", 0755)
	root, e := os.MkdirTemp("/media", "snapshot-")
	if e != nil {
		t.Fatal(e)
	}
	defer os.RemoveAll(root)
	show := filepath.Join(root, "国产剧", "Show (2026)")
	season := filepath.Join(show, "Season 1")
	os.MkdirAll(season, 0755)
	put := func(p, s string) {
		t.Helper()
		if e := os.WriteFile(p, []byte(s), 0644); e != nil {
			t.Fatal(e)
		}
	}
	put(filepath.Join(show, "tvshow.nfo"), "<tvshow><title>Show</title><plot>Series plot</plot></tvshow>")
	put(filepath.Join(season, "tvshow.nfo"), "<tvshow><title>Show</title></tvshow>")
	poster := filepath.Join(show, "season01-poster.jpg")
	put(poster, "poster")
	episode := filepath.Join(season, "Show.S01E01.strm")
	put(episode, "https://example.com/a.mkv")
	nfo := strings.TrimSuffix(episode, ".strm") + ".nfo"
	put(nfo, "<episodedetails><title>First</title><plot>First plot</plot></episodedetails>")
	if _, e := a.db.Exec("INSERT INTO libraries(id,name,path,kind) VALUES('l','test',?,'tvshows')", root); e != nil {
		t.Fatal(e)
	}
	scan := func(incremental bool) {
		t.Helper()
		a.scanLibraryMode("l", incremental)
		var err string
		a.db.QueryRow("SELECT error FROM libraries WHERE id='l'").Scan(&err)
		if err != "" {
			t.Fatal(err)
		}
	}
	scan(true)
	var seriesPath, seasonPoster, seen string
	a.db.QueryRow("SELECT path FROM items WHERE kind='Series'").Scan(&seriesPath)
	if seriesPath != show {
		t.Fatalf("wrong series %s", seriesPath)
	}
	a.db.QueryRow("SELECT poster FROM items WHERE kind='Season'").Scan(&seasonPoster)
	if seasonPoster != poster {
		t.Fatalf("missing root season poster %s", seasonPoster)
	}
	a.db.QueryRow("SELECT seen FROM items WHERE kind='Episode'").Scan(&seen)
	scan(true)
	var second string
	a.db.QueryRow("SELECT seen FROM items WHERE kind='Episode'").Scan(&second)
	if seen != second {
		t.Fatal("unchanged item was reprocessed")
	}
	put(nfo, "<episodedetails><title>First</title><plot>Changed plot, sidecar only</plot></episodedetails>")
	scan(true)
	var plot string
	a.db.QueryRow("SELECT overview FROM items WHERE kind='Episode'").Scan(&plot)
	if plot != "Changed plot, sidecar only" {
		t.Fatal(plot)
	}
	localPoster := filepath.Join(season, "poster.jpg")
	put(localPoster, "local poster")
	scan(false)
	a.db.QueryRow("SELECT poster FROM items WHERE kind='Season'").Scan(&seasonPoster)
	if seasonPoster != localPoster {
		t.Fatal("local season poster missing")
	}
	put(filepath.Join(season, "Show.S01E02.strm"), "https://example.com/b.mkv")
	os.Remove(episode)
	scan(true)
	var count int
	a.db.QueryRow("SELECT count(*) FROM items WHERE kind='Episode'").Scan(&count)
	if count != 1 {
		t.Fatalf("deletion count %d", count)
	}
	var raw string
	if e := a.db.QueryRow("SELECT data FROM scan_snapshots WHERE lib='l'").Scan(&raw); e != nil {
		t.Fatal(e)
	}
	os.Rename(root, root+"-offline")
	a.scanLibraryMode("l", true)
	os.Rename(root+"-offline", root)
	var after string
	a.db.QueryRow("SELECT data FROM scan_snapshots WHERE lib='l'").Scan(&after)
	if raw != after {
		t.Fatal("failed scan replaced snapshot")
	}
}
func TestPauseResumeScan(t *testing.T) {
	a := testApp(t)
	job := a.newActivity("update", "l", "test")
	w := httptest.NewRecorder()
	a.scanControlAPI(w, httptest.NewRequest("PUT", "/admin/scan-control", strings.NewReader(`{"Paused":true}`)))
	done := make(chan struct{})
	go func() { a.waitScan(job); close(done) }()
	select {
	case <-done:
		t.Fatal("pause did not block")
	case <-time.After(30 * time.Millisecond):
	}
	a.scanControlAPI(httptest.NewRecorder(), httptest.NewRequest("PUT", "/admin/scan-control", strings.NewReader(`{"Paused":false}`)))
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("resume did not unblock")
	}
}
func TestDetailedProbeValues(t *testing.T) {
	if n := parseFrameRate("30000/1001"); n < 29.97 || n > 29.971 {
		t.Fatal(n)
	}
	if parseFrameRate("0/0") != 0 || pixelBitDepth("yuv420p") != 8 || pixelBitDepth("yuv420p10le") != 10 {
		t.Fatal("invalid stream values")
	}
}
