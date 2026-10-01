package main

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/png"
	"net/http/httptest"
	"testing"
	"time"
)

func TestImageUploadCompatibility(t *testing.T) {
	a, _, _ := catalogFixture(t)
	if _, err := a.db.Exec("INSERT INTO api_keys VALUES('cover-key','NanShare',?,?)", digest("cover-key"), time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	var img bytes.Buffer
	png.Encode(&img, image.NewRGBA(image.Rect(0, 0, 3, 2)))
	request := func(path, key string, b []byte) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", path, bytes.NewReader(b))
		r.Header.Set("X-Emby-Token", key)
		r.Header.Set("Content-Type", "image/png")
		w := httptest.NewRecorder()
		a.serve(w, r)
		return w
	}
	old := a.imageTag("l", "Primary", a.imagePath("l", "Primary"))
	for _, path := range []string{"/emby/Items/l/Images/Primary", "/Items/l/Images/Primary/0", "/emby/items/m/images/primary?Index=0"} {
		for _, b := range [][]byte{img.Bytes(), []byte(base64.StdEncoding.EncodeToString(img.Bytes()))} {
			w := request(path, "cover-key", b)
			if w.Code != 204 {
				t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
			}
		}
	}
	if old == a.imageTag("l", "Primary", a.imagePath("l", "Primary")) {
		t.Fatal("image tag did not change")
	}
	w := browseGet(a, "/Items/l/Images/Primary", true)
	if w.Code != 200 || !bytes.Equal(w.Body.Bytes(), img.Bytes()) {
		t.Fatal("uploaded cover readback mismatch")
	}
	for _, tc := range []struct {
		path, key string
		data      []byte
		code      int
	}{
		{"/Items/l/Images/Primary", "", img.Bytes(), 401},
		{"/Items/missing/Images/Primary", "cover-key", img.Bytes(), 404},
		{"/Items/l/Images/Primary", "cover-key", []byte("invalid"), 400},
		{"/Items/l/Images/Backdrop", "cover-key", img.Bytes(), 400},
		{"/Items/l/Images/Primary/1", "cover-key", img.Bytes(), 400},
		{"/Items/l/Images/Primary?Index=1", "cover-key", img.Bytes(), 400},
		{"/Items/l/Images/Primary", "cover-key", bytes.Repeat([]byte("a"), 8<<20), 413},
	} {
		if w := request(tc.path, tc.key, tc.data); w.Code != tc.code {
			t.Fatalf("%s got %d want %d", tc.path, w.Code, tc.code)
		}
	}
	w = httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/Items/l/Images/Primary", bytes.NewReader(img.Bytes()))
	a.imageUploadRoute(w, r, User{}, r.URL.Path)
	if w.Code != 403 {
		t.Fatal("ordinary user upload allowed")
	}
	if w := request("/Items/l/Images/Primary", "browse-test", img.Bytes()); w.Code != 204 {
		t.Fatal("administrator upload failed")
	}
}
