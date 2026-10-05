package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRemoteMediaSize(t *testing.T) {
	for _, tt := range []struct {
		status   int
		cr       string
		expected int64
	}{{206, "bytes 0-0/9876543210", 9876543210}, {206, "bytes 0-0/*", 0}, {403, "", 0}} {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Range") != "bytes=0-0" || r.UserAgent() != "test-player" {
				t.Error("missing range/UA")
			}
			w.Header().Set("Content-Range", tt.cr)
			w.WriteHeader(tt.status)
		}))
		if got := remoteMediaSize(context.Background(), s.URL, "test-player"); got != tt.expected {
			t.Fatal(got, tt.expected)
		}
		s.Close()
	}
}
