package main

import (
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Read the first response without automatically following its redirect.
var sourceRedirectClient = &http.Client{
	Transport: &http.Transport{MaxIdleConnsPerHost: 16},
	Timeout:   20 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// Compare hosts including ports, ignoring case.
func sameSourceOrigin(first, second string) bool {
	firstURL, err := url.Parse(first)
	if err != nil {
		return false
	}
	secondURL, err := url.Parse(second)
	return err == nil && strings.EqualFold(firstURL.Host, secondURL.Host)
}

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
		status:      response.StatusCode,
		location:    response.Header.Get("Location"),
		contentType: response.Header.Get("Content-Type"),
	}
}
