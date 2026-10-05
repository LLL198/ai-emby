package security

import (
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestTokenRejectsConflictingQueryCredentials(t *testing.T) {
	for _, name := range []string{"api_key", "X-Emby-Token", "X-MediaBrowser-Token", "X-Emby-Authorization", "X-Emby-Api-Key"} {
		valid := "viewer-token"
		if name == "X-Emby-Authorization" {
			valid = `MediaBrowser Token="viewer-token"`
		}
		for _, other := range []string{"invalid", ""} {
			for _, alias := range []string{name, strings.ToLower(name), strings.ToUpper(name)} {
				target := "/Videos/movie/stream?" + url.QueryEscape(name) + "=" + url.QueryEscape(valid) + "&" + url.QueryEscape(alias) + "=" + url.QueryEscape(other)
				r := httptest.NewRequest("GET", target, nil)
				// An otherwise valid header cannot make an ambiguous request safe.
				r.Header.Set("X-Emby-Token", "viewer-token")
				if Token(r) != "" {
					t.Fatalf("conflicting %s credential accepted", name)
				}
			}
		}
		target := "/Videos/movie/stream?" + url.QueryEscape(name) + "=" + url.QueryEscape(valid) + "&" + url.QueryEscape(strings.ToUpper(name)) + "=" + url.QueryEscape(valid)
		if Token(httptest.NewRequest("GET", target, nil)) != "viewer-token" {
			t.Fatalf("identical repeated %s credential rejected", name)
		}
	}
}

func TestTokenPreservesCredentialPrecedence(t *testing.T) {
	r := httptest.NewRequest("GET", "/Videos/movie/stream?API_KEY=query-token&path=one&path=two", nil)
	r.Header.Set("X-Emby-Token", "header-token")
	if Token(r) != "header-token" {
		t.Fatal("normal credential precedence or unrelated parameters changed")
	}
	r.Header.Del("X-Emby-Token")
	if Token(r) != "query-token" {
		t.Fatal("case-insensitive player credential rejected")
	}
}
