package main

import (
	"net"
	"net/http"
	"strings"
)

func backendPlaybackSourceLookup(r *http.Request) bool {
	if r == nil || r.Method != http.MethodGet || device(r) != "" || field(r, "Client") != "" || field(r, "Device") != "" {
		return false
	}
	for _, name := range []string{"Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "X-Real-IP", "X-Emby-Authorization", "X-MediaBrowser-Token", "X-Emby-Token", "Authorization"} {
		if r.Header.Get(name) != "" {
			return false
		}
	}
	if q(r, "api_key") == "" {
		return false
	}
	ua := strings.ToLower(r.UserAgent())
	if ua != "" && !strings.HasPrefix(ua, "nginx/") {
		return false
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate())
}
