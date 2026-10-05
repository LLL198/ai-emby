package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func sourceRedirectStatus(status int) bool {
	return status == 301 || status == 302 || status == 303 || status == 307 || status == 308
}

func recordSourceRedirect(r *http.Request, source sourceRedirectResult) {
	trace, _ := r.Context().Value(redirectTraceKey{}).(*redirectTrace)
	if trace == nil {
		return
	}
	redirect := "no"
	if sourceRedirectStatus(source.status) && source.location != "" {
		redirect = "yes"
	}
	trace.source = append(trace.source, fmt.Sprintf("source status=%d source redirect=%s", source.status, redirect))
}

func redirectLogURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "[invalid URL]"
	}
	parsed.User = nil
	if parsed.RawQuery != "" {
		parsed.RawQuery = "[REDACTED]"
	}
	if parsed.Opaque != "" {
		parsed.Opaque = "[REDACTED]"
	}
	return parsed.String()
}

// Captures only existing responses: no network probes, database writes or body buffering on success.
func (a *App) beginRedirectLog(w *http.ResponseWriter, r *http.Request) func() {
	if !isVideoRequest(r.URL.Path) || strings.EqualFold(q(r, "GoEmbyProbe"), "true") {
		return nil
	}
	started := time.Now()
	tracked := &debugResponse{ResponseWriter: *w}
	*w = tracked
	return func() {
		elapsed := time.Since(started)
		_ = http.NewResponseController(tracked.ResponseWriter).Flush()
		status := tracked.status
		if status == 0 {
			status = 200
		}
		state := "complete"
		detail := M{"Method": r.Method, "Request": redirectLogURL(r.URL.String()), "Client": r.UserAgent(), "RemoteAddr": r.RemoteAddr, "Status": status, "Location": redirectLogURL(tracked.Header().Get("Location")), "DurationMs": float64(elapsed.Microseconds()) / 1000}
		if trace, ok := r.Context().Value(redirectTraceKey{}).(*redirectTrace); ok {
			detail["Source"] = trace.source
			if trace.fallback != "" {
				detail["Fallback"] = trace.fallback
			}
		}
		protectedStream := tracked.Header().Get("X-AI-Emby-Protected-Playback") == "true" && (status == http.StatusOK || status == http.StatusPartialContent)
		if (!protectedStream && (status < 300 || status >= 400 || tracked.Header().Get("Location") == "")) || tracked.writeErr != nil {
			state = "error"
		}
		if tracked.writeErr != nil {
			detail["WriteError"] = safeProxyError(tracked.writeErr)
		}
		if r.Context().Err() != nil {
			detail["ContextError"] = r.Context().Err().Error()
			state = "error"
		}
		if tracked.data.Len() > 0 && !tracked.truncated {
			var v any
			if json.Unmarshal(tracked.data.Bytes(), &v) == nil {
				detail["Response"] = debugJSON(v)
			}
		}
		b, _ := json.MarshalIndent(detail, "", "  ")
		key := a.newActivity("redirect", "", fmt.Sprintf("播放重定向 · HTTP %d", status))
		a.changeActivity(key, func(v *activityEntry) { v.State = state; v.Current = string(b); v.Started = started })
	}
}
