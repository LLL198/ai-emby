package main

import "net/url"

func fastHTTPSource(source string) bool {
	parsed, err := url.Parse(source)
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Hostname() != "" && parsed.User == nil && !embyVideoLocation(parsed.Path)
}
