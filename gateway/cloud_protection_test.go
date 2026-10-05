package main

import (
	"net/http/httptest"
	"testing"
)

func TestPublicGatewayRejectsCloudInternalCredentials(t *testing.T) {
	for _, query := range []string{"internal_token=valid-private-credential", "internal_token=", "internal_expires=1800000000", "internal_token=forged&internal_token=valid", "%69nternal_token=valid"} {
		r := httptest.NewRequest("GET", "/cloud/resolve/mount?"+query, nil)
		r.RemoteAddr = "127.0.0.1:1234"
		r.Header.Set("X-Emby-Token", "viewer")
		w := httptest.NewRecorder()
		if !rejectCloudInternalCredentials(w, r) || w.Code != 403 || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("private task credential accepted at public gateway: %s", query)
		}
	}
	w := httptest.NewRecorder()
	if rejectCloudInternalCredentials(w, httptest.NewRequest("GET", "/cloud/resolve/mount?path=movie&sign=legacy", nil)) {
		t.Fatal("legacy source blocked while protection is disabled")
	}
}
