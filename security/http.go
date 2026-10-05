package security

import (
	"net/http"
	"regexp"
	"strings"
)

var tokenField = regexp.MustCompile(`(?i)Token\s*=\s*"([^"]*)"`)

func Token(r *http.Request) string {
	// Reject conflicting copies before choosing a credential. Map iteration must
	// never let the gateway count one account while playback authenticates another.
	credentials := make(map[string]string)
	for key, values := range r.URL.Query() {
		key = strings.ToLower(key)
		switch key {
		case "api_key", "x-emby-token", "x-mediabrowser-token", "x-emby-authorization", "x-emby-api-key":
		default:
			continue
		}
		for _, value := range values {
			if previous, exists := credentials[key]; exists && previous != value {
				return ""
			}
			credentials[key] = value
		}
	}
	query := func(name string) string { return credentials[strings.ToLower(name)] }
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
