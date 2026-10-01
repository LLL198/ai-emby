package main

import "net/url"

// 0x797d80. The remaining NanShare resolver/cache functions are still pending.
func fastHTTPSource(source string) bool {
	parsed, err := url.Parse(source)
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Hostname() != "" && parsed.User == nil && !embyVideoLocation(parsed.Path)
}
