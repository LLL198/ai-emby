package main

import (
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

func (a *App) thirdPartyProxyPorts() []int {
	ports := []int{}
	var raw string
	if a.db != nil && a.db.QueryRow("SELECT v FROM settings WHERE k='third_party_proxy_ports'").Scan(&raw) == nil {
		if json.Unmarshal([]byte(raw), &ports) != nil || ports == nil {
			return []int{}
		}
	}
	return ports
}

// Only the direct application entrance needs forwarding. Requests already at
// a third-party entrance (including Xiaoya) keep their original routing.
func (a *App) thirdPartyPlaybackURL(r *http.Request) string {
	ports := a.thirdPartyProxyPorts()
	if len(ports) == 0 {
		return ""
	}
	host := r.Host
	if h := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Host"), ",")[0]); h != "" && !strings.ContainsAny(h, "/\\?#@ \t\r\n") {
		host = h
	}
	u, err := url.Parse("http://" + host)
	if err != nil || u.Hostname() == "" || u.Port() != "8097" {
		return ""
	}
	// Internal tool callbacks must resolve directly rather than redirect to a
	// loopback address that would be unusable by the viewer.
	ip := net.ParseIP(u.Hostname())
	if strings.EqualFold(u.Hostname(), "localhost") || (ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified())) {
		return ""
	}
	scheme := "http"
	if r.TLS != nil || strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0]) == "https" {
		scheme = "https"
	}
	return scheme + "://" + net.JoinHostPort(u.Hostname(), strconv.Itoa(ports[0])) + r.URL.RequestURI()
}
