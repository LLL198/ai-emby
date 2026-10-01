package main

import (
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestThirdPartyPlaybackPrefixAndAuthentication(t *testing.T) {
	a := testApp(t)
	var uid string
	if err := a.db.QueryRow("SELECT id FROM users LIMIT 1").Scan(&uid); err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{"INSERT INTO libraries(id,name,path,kind) VALUES('l','test','/media/test','movies')", "INSERT INTO items(id,lib,parent,name,kind,path,url,seen) VALUES('i','l','l','movie','Movie','/media/test/a.strm','http://origin/movie.mp4','g')"} {
		if _, err := a.db.Exec(sql); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := a.db.Exec("INSERT INTO tokens VALUES(?,?,?,?)", digest("viewer"), uid, "phone", time.Now().Unix()+60); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PUBLIC_PLAYBACK_URL", "http://gateway:7799")
	for _, prefix := range []string{"", "/emby", "/emby/emby", "/EMBY/emby"} {
		w := httptest.NewRecorder()
		a.serve(w, httptest.NewRequest("POST", prefix+"/Items/i/PlaybackInfo?api_key=viewer", strings.NewReader(`{}`)))
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", prefix, w.Code, w.Body.String())
		}
		var info struct {
			MediaSources []struct {
				DirectStreamUrl string
				Path            string
			}
		}
		if err := json.Unmarshal(w.Body.Bytes(), &info); err != nil {
			t.Fatal(err)
		}
		src := info.MediaSources[0]
		if strings.HasPrefix(src.DirectStreamUrl, "/emby/") {
			t.Fatal("relative stream path must not duplicate client base prefix")
		}
		if !strings.HasPrefix(src.Path, "http://") {
			t.Fatal("direct-play Path must remain absolute")
		}
		for _, method := range []string{"GET", "HEAD"} {
			r := httptest.NewRequest(method, prefix+src.DirectStreamUrl, nil)
			w = httptest.NewRecorder()
			a.serve(w, r)
			if w.Code != 302 {
				t.Fatalf("stream %s %s: %d", method, prefix, w.Code)
			}
			u, err := url.Parse(w.Header().Get("Location"))
			if err != nil || u.Host != "gateway:7799" {
				t.Fatal("stream bypassed gateway")
			}
		}
		for _, token := range []string{"", "invalid", "viewer"} {
			r := httptest.NewRequest("GET", "/auth/playback", nil)
			r.Header.Set("X-Go-Emby-URI", prefix+"/Videos/i/stream.mp4?api_key="+token)
			w = httptest.NewRecorder()
			a.serve(w, r)
			want := 401
			if token == "viewer" {
				want = 204
			}
			if w.Code != want {
				t.Fatalf("auth %s: got %d want %d", prefix, w.Code, want)
			}
		}
	}
}

func TestThirdPartyUserConfigurationShape(t *testing.T) {
	a := testApp(t)
	m := a.userDTO(User{})
	config := m["Configuration"].(M)
	if config["SubtitleMode"] != "Smart" {
		t.Fatal("missing subtitle mode")
	}
	for _, k := range []string{"PlayDefaultAudioTrack", "EnableNextEpisodeAutoPlay", "RememberAudioSelections", "RememberSubtitleSelections"} {
		if _, ok := config[k].(bool); !ok {
			t.Fatalf("missing boolean %s", k)
		}
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), `"LatestItemsExcludes":null`) || strings.Contains(string(b), `"EnabledDevices":null`) {
		t.Fatal("client arrays must not be null")
	}
	policy := m["Policy"].(M)
	for _, k := range []string{"EnableContentDownloading", "EnableSubtitleDownloading", "EnablePlaybackRemuxing", "EnableVideoPlaybackTranscoding"} {
		if policy[k] != false {
			t.Fatalf("unexpected capability %s", k)
		}
	}
}
