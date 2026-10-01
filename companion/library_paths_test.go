package main

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFolderRemovalPreservesFilesAndOtherIndexes(t *testing.T) {
	a := testApp(t)
	root := t.TempDir()
	one := filepath.Join(root, "one_%")
	two := one + "-other"
	os.MkdirAll(one, 0755)
	os.MkdirAll(two, 0755)
	for _, p := range []string{one, two} {
		if e := os.WriteFile(filepath.Join(p, "movie.strm"), []byte("http://example.com/video.mp4"), 0644); e != nil {
			t.Fatal(e)
		}
	}
	_, e := a.db.Exec("INSERT INTO libraries(id,name,path,kind) VALUES('l','test',?,'movies')", one)
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal([]string{one, two})
	a.db.Exec("INSERT INTO settings VALUES('library-paths:l',?)", string(raw))
	a.scanLibrary("l")
	var count int
	a.db.QueryRow("SELECT count(*) FROM items WHERE lib='l'").Scan(&count)
	if count != 2 {
		t.Fatalf("multi root scan count %d", count)
	}
	b, _ := json.Marshal(M{"ID": "l", "Path": one})
	w := httptest.NewRecorder()
	a.libraryFolders(w, httptest.NewRequest("DELETE", "/admin/library-folders", strings.NewReader(string(b))))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if _, e = os.Stat(filepath.Join(one, "movie.strm")); e != nil {
		t.Fatal("source removed", e)
	}
	a.scanLibrary("l")
	a.db.QueryRow("SELECT count(*) FROM items WHERE lib='l'").Scan(&count)
	if count != 1 {
		t.Fatalf("removed index restored or neighbor deleted: %d", count)
	}
	b, _ = json.Marshal(M{"ID": "l", "Path": two})
	w = httptest.NewRecorder()
	a.libraryFolders(w, httptest.NewRequest("DELETE", "/admin/library-folders", strings.NewReader(string(b))))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	a.scanLibrary("l")
	if paths := a.libraryPaths("l", one); len(paths) != 0 {
		t.Fatal(paths)
	}
	a.db.QueryRow("SELECT count(*) FROM items WHERE lib='l'").Scan(&count)
	if count != 0 {
		t.Fatal(count)
	}
}
func TestFolderMutationBlockedDuringScan(t *testing.T) {
	a := testApp(t)
	a.scan.Lock()
	defer a.scan.Unlock()
	w := httptest.NewRecorder()
	a.libraryFolders(w, httptest.NewRequest("DELETE", "/admin/library-folders", strings.NewReader(`{"ID":"l","Path":"/media/a"}`)))
	if w.Code != 409 {
		t.Fatal(w.Code)
	}
}

func TestFolderAdditionValidationAndScan(t *testing.T) {
	a := testApp(t)
	if e := os.MkdirAll("/media", 0755); e != nil {
		t.Skip(e)
	}
	base, e := os.MkdirTemp("/media", "go-emby-folders-test-")
	if e != nil {
		t.Skip(e)
	}
	defer os.RemoveAll(base)
	first := filepath.Join(base, "first")
	second := filepath.Join(base, "second")
	os.MkdirAll(first, 0755)
	os.MkdirAll(second, 0755)
	os.WriteFile(filepath.Join(second, "movie.strm"), []byte("http://example.com/movie.mp4"), 0644)
	_, e = a.db.Exec("INSERT INTO libraries(id,name,path,kind) VALUES('l','test',?,'movies')", first)
	if e != nil {
		t.Fatal(e)
	}
	call := func(path string) int {
		b, _ := json.Marshal(M{"ID": "l", "Path": path})
		w := httptest.NewRecorder()
		a.libraryFolders(w, httptest.NewRequest("POST", "/admin/library-folders", strings.NewReader(string(b))))
		return w.Code
	}
	if code := call(base); code != 409 {
		t.Fatalf("overlap %d", code)
	}
	outside := t.TempDir()
	os.Symlink(outside, filepath.Join(base, "escape"))
	if code := call(filepath.Join(base, "escape")); code != 400 {
		t.Fatalf("symlink escape %d", code)
	}
	if code := call(second); code != 200 {
		t.Fatalf("add %d", code)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		var count int
		a.db.QueryRow("SELECT count(*) FROM items WHERE lib='l'").Scan(&count)
		if count == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("new folder not automatically scanned")
		}
		time.Sleep(10 * time.Millisecond)
	}
	a.scan.Lock()
	a.scan.Unlock()
	if paths := a.libraryPaths("l", first); len(paths) != 2 {
		t.Fatal(paths)
	}
	if code := call(second); code != 409 {
		t.Fatalf("duplicate %d", code)
	}
}
