package main

import (
 "encoding/json"
 "net/http/httptest"
 "testing"
)

func TestEndpointNetwork(t *testing.T) {
 for _, tc := range []struct{peer, forwarded string; local, network bool}{
 {"127.0.0.1:123", "", true, true},
 {"[::1]:123", "", true, true},
 {"192.168.1.5:123", "", false, true},
 {"203.0.113.5:123", "127.0.0.1", false, false},
 {"172.18.0.1:123", "203.0.113.5", false, false},
 {"172.18.0.1:123", "127.0.0.1, 203.0.113.5", false, false},
 {"172.18.0.1:123", "192.168.1.5", false, true},
 {"invalid", "", false, false},
 } {
 r := httptest.NewRequest("GET", "/emby/System/Endpoint", nil)
 r.RemoteAddr = tc.peer
 r.Header.Set("X-Forwarded-For", tc.forwarded)
 m := endpointInfo(r)
 if m["IsLocal"] != tc.local || m["IsInNetwork"] != tc.network { t.Fatalf("%+v: %v", tc, m) }
 }
}

func TestEndpointAuthenticatedRoute(t *testing.T) {
 a, _, _ := catalogFixture(t)
 for _, path := range []string{"/System/Endpoint", "/emby/System/Endpoint", "/emby/emby/system/endpoint/"} {
 r := httptest.NewRequest("GET", path+"?X-Emby-Token=browse-test", nil)
 r.Header.Set("Origin", "emby-local://app")
 w := httptest.NewRecorder(); a.serve(w,r)
 var m map[string]bool
 if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &m) != nil || len(m) != 2 || w.Header().Get("Access-Control-Allow-Credentials") != "true" { t.Fatal(w.Code,w.Body.String()) }
 }
 w := httptest.NewRecorder(); a.serve(w,httptest.NewRequest("GET","/emby/System/Endpoint",nil))
 if w.Code != 401 { t.Fatal(w.Code) }
 w = httptest.NewRecorder(); a.serve(w,httptest.NewRequest("POST","/emby/System/Endpoint?X-Emby-Token=browse-test",nil))
 if w.Code != 405 { t.Fatal(w.Code) }
}
