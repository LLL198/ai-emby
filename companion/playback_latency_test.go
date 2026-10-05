package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"
)

func TestPlaybackInfoDoesNotWaitForMetadata(t *testing.T) {
	a := testApp(t)
	x := probeFixture(t, a)
	// Occupy the worker so metadata cannot complete during the request.
	a.probes.running = 1
	var uid string
	if err := a.db.QueryRow("SELECT id FROM users LIMIT 1").Scan(&uid); err != nil {
		t.Fatal(err)
	}
	u := User{ID: uid, Device: "latency-test", Max: 5}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	r := httptest.NewRequest("POST", "/Items/i/PlaybackInfo?api_key=viewer", nil).WithContext(ctx)
	w := httptest.NewRecorder()
	start := time.Now()
	a.playback(w, r, u, x.ID)
	elapsed := time.Since(start)
	if elapsed > time.Second {
		t.Fatalf("PlaybackInfo waited for metadata: %s", elapsed)
	}
	if w.Code != 200 {
		t.Fatalf("HTTP %d: %s", w.Code, w.Body.String())
	}
	var result struct {
		MediaSources []struct{ DirectStreamUrl string }
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.MediaSources) == 0 || result.MediaSources[0].DirectStreamUrl == "" {
		t.Fatal("missing playback URL")
	}
	if len(a.probes.queue) != 0 {
		t.Fatal("playback must not enqueue metadata")
	}
	t.Logf("PlaybackInfo returned in %s without probing", elapsed)
}
