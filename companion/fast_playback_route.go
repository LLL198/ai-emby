package main

import (
	"net/url"
	"strings"
)

// 0x79b7c0. Keep resolver-specific URLs unchanged.
func fastPlaybackSourceURL(playbackURL, source string) string {
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(source)), "ed2k://") {
		return playbackURL
	}
	if parsed, err := url.Parse(source); err == nil && strings.EqualFold(strings.TrimRight(parsed.Path, "/"), "/api/ed2k_strm") {
		return playbackURL
	}
	return fastPlaybackURL(playbackURL)
}

// 0x79b8e0. Only rewrite relative, recognized direct-play routes; preserve
// query parameters and clear the old escaped path after changing Path.
func fastPlaybackURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "" || parsed.Host != "" {
		return raw
	}
	parts := strings.Split(embyPath(parsed.Path), "/")
	if len(parts) != 4 || !strings.EqualFold(parts[1], "videos") || parts[2] == "" || !isStreamRoute(parts[3]) {
		return raw
	}
	parsed.Path = "/Items/" + parts[2] + "/Playback/" + parts[3]
	parsed.RawPath = ""
	return parsed.String()
}

// 0x79baa0.
func fastPlaybackItem(path string) (string, bool) {
	parts := strings.Split(embyPath(path), "/")
	if len(parts) == 5 && strings.EqualFold(parts[1], "items") && parts[2] != "" && strings.EqualFold(parts[3], "playback") && isStreamRoute(parts[4]) {
		return parts[2], true
	}
	return "", false
}
