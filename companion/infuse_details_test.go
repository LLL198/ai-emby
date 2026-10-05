package main

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestInfuseEpisodeDetailsAndBackgroundQueue(t *testing.T) {
	a, uid, _ := catalogFixture(t)
	a.db.Exec("UPDATE items SET url=url || id WHERE kind='Episode'")
	a.probes.running = 1
	r := httptest.NewRequest("GET", "/Shows/s/Episodes?UserId="+uid+"&ExcludeLocationTypes=Virtual&Fields=Etag,MediaSources,AlternateMediaSources,Genres,Overview,ParentId,ProviderIds", nil)
	r.Header.Set("X-Emby-Authorization", `MediaBrowser Version="8.5.3", DeviceId="browse-device", Device="iPhone", Client="Infuse-Direct", Token="browse-test"`)
	r.Header.Set("User-Agent", "Infuse-Direct/8.5.3")
	w := httptest.NewRecorder()
	a.serve(w, r)
	var body struct {
		Items            []M
		TotalRecordCount int
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if body.TotalRecordCount != 2 || len(body.Items) != 2 {
		t.Fatal(body)
	}
	for _, m := range body.Items {
		for _, key := range []string{"Etag", "Genres", "ProviderIds", "Overview", "ParentId", "SeriesId", "SeriesName", "SeasonId", "SeasonName", "MediaSources"} {
			if _, ok := m[key]; !ok {
				t.Errorf("missing %s", key)
			}
		}
		if m["SeriesId"] != "s" {
			t.Fatal(m)
		}
		sources := m["MediaSources"].([]any)
		if len(sources) != 1 {
			t.Fatal(m)
		}
		if sources[0].(map[string]any)["SupportsTranscoding"] != false {
			t.Fatal("transcoding enabled")
		}
	}
	if len(a.probes.queue) != 0 || a.probes.running != 1 {
		t.Fatal("episode list started extraction", len(a.probes.queue), a.probes.running)
	}
	a.serve(httptest.NewRecorder(), r)
	if len(a.probes.queue) != 0 {
		t.Fatal("repeated episode list started extraction")
	}
}

func TestRemovedStagedSettingIgnored(t *testing.T) {
	a := testApp(t)
	if _, err := a.db.Exec("INSERT INTO settings VALUES('media_info',?) ON CONFLICT(k) DO UPDATE SET v=excluded.v", `{"Staged":true,"Browse":true,"Concurrency":3}`); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(a.probeSettings())
	if strings.Contains(string(b), `"Staged":`) || a.probeSettings().Concurrency != 3 {
		t.Fatal(string(b))
	}
}
