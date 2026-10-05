package main

import (
	"net/http/httptest"
	"testing"
)

func TestGatewayRejectsAmbiguousPlaybackCredentials(t *testing.T) {
	db := antiTheftGatewayDB(t)
	for _, query := range []string{
		"api_key=invalid&API_KEY=viewer-token",
		"API_KEY=viewer-token&api_key=invalid",
		"api_key=viewer-token&api_key=invalid",
		"%61pi_key=invalid&API_KEY=viewer-token",
	} {
		r := httptest.NewRequest("GET", "/Videos/i/stream?"+query, nil)
		w := httptest.NewRecorder()
		if enforceAntiTheft(db, w, r) {
			t.Fatal("ambiguous credentials must fail authentication, not ban an account")
		}
		if _, authenticated := featureGatewayIdentity(db, r); authenticated || featureGatewayToken(r) != "" {
			t.Fatal("ambiguous request authenticated after skipping account counting")
		}
	}
	var accounts int
	if err := db.QueryRow("SELECT count(*) FROM anti_theft_accounts").Scan(&accounts); err != nil || accounts != 0 {
		t.Fatal("invalid credential probes changed account counters", accounts, err)
	}
	for i := 0; i < 2; i++ {
		r := httptest.NewRequest("GET", "/Videos/i/stream?api_key=viewer-token&API_KEY=viewer-token", nil)
		w := httptest.NewRecorder()
		blocked := enforceAntiTheft(db, w, r)
		if blocked != (i == 1) {
			t.Fatal("identical repeated credentials bypassed the account limit")
		}
	}
}
