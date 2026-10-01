package main

import (
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client at 0x1260980, transport at 0x1266840; callback 0x766240 returns
// http.ErrUseLastResponse. Closing a successful response never relays its body.
var sourceRedirectClient = &http.Client{
	Transport: &http.Transport{MaxIdleConnsPerHost: 16},
	Timeout:   20 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// 0x837d20. Despite the original name, only Host is compared (including port).
func sameSourceOrigin(first, second string) bool {
	a, aerr := url.Parse(first)
	b, berr := url.Parse(second)
	return aerr == nil && berr == nil && strings.EqualFold(a.Host, b.Host)
}

// 0x837de0.
func fetchSourceRedirect(r *http.Request, source string) sourceRedirectResult {
	if !fastHTTPSource(source) {
		return sourceRedirectResult{}
	}
	parsed, _ := url.Parse(source)
	if strings.EqualFold(parsed.Host, r.Host) {
		return sourceRedirectResult{}
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, source, nil)
	if err != nil {
		return sourceRedirectResult{}
	}
	for _, key := range []string{"Range", "User-Agent", "Accept", "Accept-Encoding", "Icy-Metadata"} {
		if values := r.Header.Values(key); len(values) != 0 {
			req.Header[key] = append([]string(nil), values...)
		}
	}
	req.Header.Set("X-Go-Emby-Fast-Path-Hop", "1")
	response, err := sourceRedirectClient.Do(req)
	if err != nil {
		return sourceRedirectResult{}
	}
	defer response.Body.Close()
	return sourceRedirectResult{
		status: response.StatusCode, location: response.Header.Get("Location"), contentType: response.Header.Get("Content-Type"),
	}
}
