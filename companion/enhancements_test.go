package main

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEnhancementToggle(t *testing.T) {
	a, _, _ := catalogFixture(t)
	x, e := a.item("m")
	if e != nil {
		t.Fatal(e)
	}
	if !a.hideMissingActors() {
		t.Fatal("default disabled")
	}
	m := a.dto(x)
	if len(m["People"].([]M)) != 0 {
		t.Fatal("missing actor not hidden")
	}
	w := httptest.NewRecorder()
	a.enhancementSettings(w, httptest.NewRequest("PUT", "/admin/enhancements", strings.NewReader(`{"HideMissingActorImages":false}`)))
	if w.Code != 200 || a.hideMissingActors() {
		t.Fatal("save failed")
	}
	if len(a.dto(x)["People"].([]M)) == 0 {
		t.Fatal("actor not restored")
	}
	a.db.Exec("UPDATE settings SET v='true' WHERE k='hide_missing_actor_images'")
	a.db.Exec("UPDATE item_people SET thumb='https://image.tmdb.org/t/p/w185/test.jpg'")
	if len(a.dto(x)["People"].([]M)) == 0 {
		t.Fatal("pictured actor hidden")
	}
}
func TestForwardedPlaybackOrigin(t *testing.T) {
	a := testApp(t)
	r := httptest.NewRequest("GET", "http://internal:8097/Items/movie/PlaybackInfo?api_key=viewer", nil)
	r.Header.Set("X-Forwarded-Proto", "https")
	r.Header.Set("X-Forwarded-Host", "media.example.com")
	m := a.viewerSource(Item{ID: "movie", URL: "http://origin/movie.mp4"}, r, User{})
	if !strings.HasPrefix(m["Path"].(string), "https://media.example.com/emby/Videos/") {
		t.Fatal(m["Path"])
	}
}
