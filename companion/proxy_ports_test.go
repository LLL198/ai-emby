package main

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProxyPortsPersistenceValidationAndRouting(t *testing.T) {
	a := testApp(t)
	for _, payload := range []string{`{"ThirdPartyProxyPorts":[0]}`, `{"ThirdPartyProxyPorts":[65536]}`, `{"ThirdPartyProxyPorts":[8097]}`, `{"ThirdPartyProxyPorts":[7799,7799]}`} {
		w := httptest.NewRecorder()
		a.enhancementSettings(w, httptest.NewRequest("PUT", "/admin/enhancements", strings.NewReader(payload)))
		if w.Code != 400 {
			t.Fatalf("invalid ports accepted: %s status %d", payload, w.Code)
		}
	}
	w := httptest.NewRecorder()
	a.enhancementSettings(w, httptest.NewRequest("PUT", "/admin/enhancements", strings.NewReader(`{"ThirdPartyProxyPorts":[7799,7789]}`)))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	ports := a.thirdPartyProxyPorts()
	if len(ports) != 2 || ports[0] != 7799 || ports[1] != 7789 {
		t.Fatal(ports)
	}
	for _, port := range []string{"7799", "7789", "2345"} {
		if got := a.thirdPartyPlaybackURL(httptest.NewRequest("GET", "http://media.example:"+port+"/Videos/a/stream?api_key=test", nil)); got != "" {
			t.Fatalf("loop: %s", got)
		}
	}
	for _, host := range []string{"127.0.0.1", "172.18.0.1", "localhost", "[::1]"} {
		if got := a.thirdPartyPlaybackURL(httptest.NewRequest("GET", "http://"+host+":8097/Videos/a/stream", nil)); got != "" {
			t.Fatal("internal callback redirected", got)
		}
	}
	r := httptest.NewRequest("GET", "http://internal:8097/Videos/a/stream?api_key=test", nil)
	r.Header.Set("X-Forwarded-Host", "media.example:8097")
	r.Header.Set("X-Forwarded-Proto", "https")
	if got := a.thirdPartyPlaybackURL(r); got != "https://media.example:7799/Videos/a/stream?api_key=test" {
		t.Fatal(got)
	}
	w = httptest.NewRecorder()
	a.enhancementSettings(w, httptest.NewRequest("PUT", "/admin/enhancements", strings.NewReader(`{"ThirdPartyProxyPorts":[]}`)))
	if w.Code != 200 || a.thirdPartyPlaybackURL(r) != "" {
		t.Fatal("clear failed")
	}
}
