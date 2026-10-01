package main

import (
	"encoding/json"
	"testing"
)

func TestLegacyPartialCacheCannotDowngradeComplete(t *testing.T) {
	a := testApp(t)
	x := probeFixture(t, a)
	if err := a.saveMedia(x, M{"Partial": true, "Size": 123}); err != nil {
		t.Fatal(err)
	}
	a.probes.running = 1
	j := a.queueProbe(x, "t", "ua", false)
	if j == nil || len(a.probes.queue) != 1 {
		t.Fatal("partial cache prevented full extraction")
	}
	if err := a.saveMedia(x, M{"Size": 456, "Container": "mkv"}); err != nil {
		t.Fatal(err)
	}
	if err := a.saveMedia(x, M{"Partial": true, "Size": 123}); err != nil {
		t.Fatal(err)
	}
	if a.cachedMedia(x)["Partial"] == true {
		t.Fatal("complete metadata downgraded")
	}
}
func TestPartialPreservesCodecs(t *testing.T) {
	dst := M{"MediaStreams": []M{{"Type": "Video", "Codec": "hevc"}, {"Type": "Audio", "Codec": "aac"}}}
	mergeCachedMedia(dst, M{"Partial": true, "Size": 123, "MediaStreams": []M{{"Type": "Video", "Width": 1920, "Height": 1080}}})
	b, _ := json.Marshal(dst["MediaStreams"])
	var streams []M
	json.Unmarshal(b, &streams)
	if len(streams) != 2 || streams[0]["Codec"] != "hevc" || streams[0]["Width"] != float64(1920) {
		t.Fatal(dst)
	}
	if _, ok := dst["Partial"]; ok {
		t.Fatal("internal cache marker exposed")
	}
}
