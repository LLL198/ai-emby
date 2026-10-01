package main

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestConfiguredMediaRoots(t *testing.T) {
	t.Setenv("MEDIA_ROOTS", "/media:/media1:/media2:/media3")
	for _, p := range []string{"/media", "/media1/show", "/media2/a", "/media3/b"} {
		if !allowedMediaPath(p) {
			t.Fatal(p)
		}
	}
	for _, p := range []string{"/etc/passwd", "/media10/a", "/media-other/a"} {
		if allowedMediaPath(p) {
			t.Fatal(p)
		}
	}
}
func TestPublicClientStrings(t *testing.T) {
	a := testApp(t)
	for _, path := range []string{"/emby/web/strings/en-US.json", "/web/strings/zh-CN.json"} {
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("Origin", "emby-local://app")
		w := httptest.NewRecorder()
		a.serve(w, r)
		var values map[string]any
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &values) != nil || len(values) == 0 {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
	}
	w := httptest.NewRecorder()
	a.serve(w, httptest.NewRequest("GET", "/emby/Items", nil))
	if w.Code != 401 {
		t.Fatal(w.Code)
	}
}
func TestEpisodeSidecarThumb(t *testing.T) {
	a := testApp(t)
	os.MkdirAll("/media", 0755)
	dir, e := os.MkdirTemp("/media", "episode-thumb-")
	if e != nil {
		t.Fatal(e)
	}
	defer os.RemoveAll(dir)
	file := filepath.Join(dir, "S01E01.jpg")
	if e = os.WriteFile(file, []byte("test"), 0644); e != nil {
		t.Fatal(e)
	}
	_, e = a.db.Exec("INSERT INTO libraries(id,name,path,kind) VALUES('thumb-lib','Test',?,'tvshows')", dir)
	if e != nil {
		t.Fatal(e)
	}
	_, e = a.db.Exec("INSERT INTO items(id,lib,parent,name,kind,path,seen) VALUES('thumb-ep','thumb-lib','thumb-lib','Episode','Episode',?,'g')", filepath.Join(dir, "S01E01.strm"))
	if e != nil {
		t.Fatal(e)
	}
	for _, kind := range []string{"Primary", "Thumb"} {
		if got := a.imagePath("thumb-ep", kind); got != file {
			t.Fatalf("%s: %s", kind, got)
		}
	}
}
