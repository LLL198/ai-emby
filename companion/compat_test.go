package main

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestResumeIsolationAndDownloadPolicy(t *testing.T) {
	a := testApp(t)
	var uid string
	a.db.QueryRow("SELECT id FROM users LIMIT 1").Scan(&uid)
	for _, q := range []string{"INSERT INTO libraries(id,name,path,kind) VALUES('l','中国电影','/media/test','movies')", "INSERT INTO items(id,lib,parent,name,kind,path,seen) VALUES('i','l','l','看过','Movie','/media/test/a.strm','g'),('j','l','l','未看','Movie','/media/test/b.strm','g')"} {
		if _, e := a.db.Exec(q); e != nil {
			t.Fatal(e)
		}
	}
	a.db.Exec("INSERT INTO userdata(user_id,item,position) VALUES(?,'i',123)", uid)
	for _, route := range []string{"/Users/" + uid + "/Items/Resume", "/Items/Resume", "/Items?Filters=IsResumable"} {
		w := httptest.NewRecorder()
		a.items(w, httptest.NewRequest("GET", route, nil), User{ID: uid}, false)
		var b struct {
			Items            []M
			TotalRecordCount int
		}
		json.Unmarshal(w.Body.Bytes(), &b)
		if w.Code != 200 || b.TotalRecordCount != 1 || b.Items[0]["Id"] != "i" {
			t.Fatal(route, w.Body.String())
		}
		if b.Items[0]["UserData"].(map[string]any)["PlaybackPositionTicks"] != float64(123) {
			t.Fatal("lost position")
		}
	}
	w := httptest.NewRecorder()
	a.items(w, httptest.NewRequest("GET", "/Items/Resume", nil), User{ID: "another"}, false)
	if !strings.Contains(w.Body.String(), `"TotalRecordCount":0`) {
		t.Fatal(w.Body.String())
	}
	for _, p := range []string{"/Items/i/Download", "/emby/Videos/i/x/Subtitles/0/Stream.srt", "/Sync/Jobs"} {
		w := httptest.NewRecorder()
		a.serve(w, httptest.NewRequest("GET", p, nil))
		if w.Code != 403 {
			t.Fatal(p, w.Code)
		}
	}
	p := a.userDTO(User{ID: uid})["Policy"].(M)
	if p["EnableContentDownloading"] != false || p["EnableSubtitleDownloading"] != false {
		t.Fatal(p)
	}
}
func TestViewerSourceDoesNotExposeSTRM(t *testing.T) {
	a := testApp(t)
	x := Item{ID: "i", Name: "Movie", URL: "http://private-source/video.mkv", Kind: "Movie"}
	r := httptest.NewRequest("GET", "http://server:7799/Items/i?api_key=viewer", nil)
	m := a.viewerSource(x, r, User{})
	if strings.Contains(m["Path"].(string), "private-source") || !strings.Contains(m["Path"].(string), "server:7799/emby/Videos/i/") {
		t.Fatal(m)
	}
	if a.viewerSource(x, r, User{API: true})["Path"] != x.URL {
		t.Fatal("resolver lost source")
	}
}
func TestAuthenticatedResumeRoute(t *testing.T) {
	a := testApp(t)
	var uid string
	a.db.QueryRow("SELECT id FROM users LIMIT 1").Scan(&uid)
	a.db.Exec("INSERT INTO tokens VALUES(?,?,?,?)", digest("test"), uid, "device", time.Now().Unix()+60)
	r := httptest.NewRequest("GET", "/emby/Items/Resume?api_key=test", nil)
	w := httptest.NewRecorder()
	a.serve(w, r)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
}
