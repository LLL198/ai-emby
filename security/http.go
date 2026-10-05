package security

import (
	"net/http"
	"regexp"
	"strings"
)

var tokenField = regexp.MustCompile(`(?i)Token\s*=\s*"([^"]*)"`)

func Token(r *http.Request) string {
	query := func(name string) string {
		for key, values := range r.URL.Query() {
			if strings.EqualFold(key, name) && len(values) != 0 {
				return values[0]
			}
		}
		return ""
	}
	for _, token := range []string{r.Header.Get("X-Emby-Token"), r.Header.Get("X-MediaBrowser-Token"), query("api_key"), query("X-Emby-Token"), query("X-MediaBrowser-Token")} {
		if token != "" {
			return token
		}
	}
	for _, auth := range []string{r.Header.Get("X-Emby-Authorization"), r.Header.Get("Authorization"), query("X-Emby-Authorization")} {
		if match := tokenField.FindStringSubmatch(auth); len(match) == 2 && match[1] != "" {
			return match[1]
		}
	}
	for _, token := range []string{r.Header.Get("X-Emby-Api-Key"), query("X-Emby-Api-Key")} {
		if token != "" {
			return token
		}
	}
	return strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
}

func PlaybackEntry(r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	path := strings.ToLower(r.URL.Path)
	for strings.Contains(path, "//") {
		path = strings.ReplaceAll(path, "//", "/")
	}
	for strings.HasPrefix(path, "/emby/") {
		path = path[len("/emby"):]
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) >= 3 && parts[0] == "features" && parts[1] == "stream" {
		return parts[2] != ""
	}
	var route string
	if len(parts) >= 3 && (parts[0] == "videos" || parts[0] == "audio") {
		route = parts[2]
	}
	if len(parts) == 4 && parts[0] == "items" && parts[2] == "playback" {
		route = parts[3]
	}
	// The core accepts legacy video route names beginning with "stream".
	return strings.HasPrefix(route, "stream") || route == "original" || strings.HasPrefix(route, "original.") || route == "universal"
}

// These authenticated POSTs only release a session or playback reservation.
func CleanupRequest(r *http.Request) bool {
	if r.Method != http.MethodPost {
		return false
	}
	path := strings.ToLower(r.URL.Path)
	for strings.HasPrefix(path, "/emby/") {
		path = path[len("/emby"):]
	}
	return path == "/sessions/logout" || path == "/sessions/playing/stopped"
}
