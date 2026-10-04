package main

import (
	"context"
	"net/http"
	"strings"
)

func probeHostSource(ctx context.Context, source string) (string, string) {
	mapped, host := localMappedSource(ctx, source)
	if host == "" {
		return source, ""
	}
	ctx, cancel := context.WithTimeout(ctx, sourceMappingTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, mapped, nil)
	if err != nil {
		return mapped, host
	}
	request.Host = host
	response, err := sourceRedirectClient.Do(request)
	if err != nil {
		return mapped, host
	}
	response.Body.Close()
	if response.StatusCode < 300 || response.StatusCode >= 400 {
		return mapped, host
	}
	location, err := response.Location()
	if err != nil || !fastHTTPSource(location.String()) {
		return mapped, host
	}
	if strings.EqualFold(location.Host, request.URL.Host) {
		location.Host = host
	}
	if !strings.EqualFold(location.Host, host) {
		return location.String(), ""
	}
	return mapped, host
}

func (a *App) mediaProbeLog(itemID, message string) {
	activity := a.newActivity("probe", itemID, "media-probe "+probeErrorURL.ReplaceAllString(message, "[URL 已脱敏]"))
	a.changeActivity(activity, func(entry *activityEntry) { entry.State = "complete" })
}
