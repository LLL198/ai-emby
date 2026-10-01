package main

import (
 "net"
 "net/http"
 "strings"
)

// Match the existing local Caddy trust boundary used by playback logging.
func endpointInfo(r *http.Request) M {
 host, _, err := net.SplitHostPort(r.RemoteAddr)
 if err != nil { host = r.RemoteAddr }
 ip := net.ParseIP(host)
 if ip != nil && (ip.IsLoopback() || host == "172.18.0.1") {
  parts := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
  if forwarded := net.ParseIP(strings.TrimSpace(parts[len(parts)-1])); forwarded != nil { ip = forwarded }
 }
 local := ip != nil && ip.IsLoopback()
 return M{"IsLocal": local, "IsInNetwork": ip != nil && (local || ip.IsPrivate() || ip.IsLinkLocalUnicast())}
}

func serveEndpoint(w http.ResponseWriter, r *http.Request) {
 if r.Method != http.MethodGet && r.Method != http.MethodHead {
  w.Header().Set("Allow", "GET, HEAD")
  fail(w, http.StatusMethodNotAllowed, "GET or HEAD required")
  return
 }
 if r.Method == http.MethodHead { w.Header().Set("Content-Type", "application/json; charset=utf-8"); return }
 respond(w, endpointInfo(r))
}
