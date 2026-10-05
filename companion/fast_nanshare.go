package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

type fastNanShareFlight struct {
	done   chan struct{}
	result fastNanShareResult
}

type fastNanShareResult struct {
	source   sourceRedirectResult
	location string
	until    time.Time
}

type fastNanShareState struct {
	mu      sync.Mutex
	cache   map[string]fastNanShareResult
	flights map[string]*fastNanShareFlight
}

const (
	fastContinuationParameter = "GoEmbyFastWait"
	fastNanShareCapacity      = 1024
)

func (a *App) nanShareFastEnabled() bool {
	var value string
	return a.db.QueryRow("SELECT v FROM settings WHERE k='nanshare_fast_path'").Scan(&value) == nil && value == "true"
}

func (a *App) fastPathWaitSeconds() int {
	var value string
	if a.db.QueryRow("SELECT v FROM settings WHERE k='fast_path_wait_seconds'").Scan(&value) == nil {
		if seconds, err := strconv.Atoi(value); err == nil && seconds >= 1 && seconds <= 20 {
			return seconds
		}
	}
	return 5
}

func (a *App) fastNanShareLog(message string) {
	key := a.newActivity("redirect", "", "fast-path "+message)
	a.changeActivity(key, func(entry *activityEntry) { entry.State = "complete" })
}

func fastNanShareKey(r *http.Request, userID, deviceID, serverID string, item Item) string {
	requestURL := *r.URL
	if _, ok := fastPlaybackItem(requestURL.Path); ok {
		requestURL.Scheme, requestURL.Host, requestURL.Opaque = "", "", ""
		requestURL.User = nil
		requestURL.RawQuery = fastContinuationQuery(requestURL.RawQuery)
	}
	remote := r.RemoteAddr
	if host, _, err := net.SplitHostPort(remote); err == nil {
		remote = host
	}
	encoded, _ := json.Marshal([]any{serverID, userID, item.ID, q(r, "MediaSourceId"), item.Path, item.URL, deviceID, r.Method, requestURL.String(), r.Host, r.Header, remote})
	return digest(string(encoded))
}

func fastContinuationQuery(raw string) string {
	parts := strings.Split(raw, "&")
	kept := parts[:0]
	for _, part := range parts {
		key, _, _ := strings.Cut(part, "=")
		decoded, err := url.QueryUnescape(key)
		if err != nil || decoded != fastContinuationParameter {
			kept = append(kept, part)
		}
	}
	return strings.Join(kept, "&")
}

func fastContinuation(r *http.Request) (int, bool) {
	if _, ok := fastPlaybackItem(r.URL.Path); !ok {
		return 0, false
	}
	values, present := r.URL.Query()[fastContinuationParameter]
	if !present {
		return 0, true
	}
	if len(values) != 1 {
		return 0, false
	}
	n, err := strconv.Atoi(values[0])
	return n, err == nil && n >= 1 && n <= 3
}

func writeFastContinuation(w http.ResponseWriter, r *http.Request, continuation int) {
	target := *r.URL
	target.Scheme, target.Host, target.Opaque = "", "", ""
	target.User = nil
	target.OmitHost = false
	target.Fragment, target.RawFragment = "", ""
	target.RawQuery = fastContinuationQuery(target.RawQuery)
	if target.RawQuery != "" {
		target.RawQuery += "&"
	}
	target.RawQuery += fastContinuationParameter + "=" + strconv.Itoa(continuation+1)
	w.Header().Set("Location", target.String())
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusTemporaryRedirect)
}

func (a *App) tryFastNanShare(w http.ResponseWriter, r *http.Request, userID, deviceID string, item Item) bool {
	if r.Context().Err() != nil || !a.nanShareFastEnabled() || !fastHTTPSource(item.URL) {
		return false
	}
	if source, err := url.Parse(item.URL); err == nil && strings.EqualFold(strings.TrimRight(source.Path, "/"), "/api/ed2k_strm") {
		return false
	}
	if (r.Method != http.MethodGet && r.Method != http.MethodHead) || r.Header.Get("X-Go-Emby-Fast-Path-Hop") != "" || r.Host == "" || userID == "" || item.ID == "" || token(r) == "" || r.ContentLength != 0 || len(r.TransferEncoding) != 0 {
		a.fastNanShareLog("fallback")
		return false
	}
	a.fastNanShareLog("matched")
	wait := time.Duration(a.fastPathWaitSeconds()) * time.Second
	continuation, canContinue := fastContinuation(r)
	key := fastNanShareKey(r, userID, deviceID, a.serverID, item) + "|" + wait.String()
	state := &a.fastNanShare
	state.mu.Lock()
	if result, ok := state.cache[key]; ok && time.Now().Before(result.until) {
		state.mu.Unlock()
		if r.Context().Err() != nil {
			return false
		}
		recordSourceRedirect(r, result.source)
		a.fastNanShareLog("cache hit")
		writeFastNanShare(w, result.location)
		return true
	}
	flight := state.flights[key]
	if flight == nil {
		if (canContinue && continuation > 0) || len(state.flights) >= fastNanShareCapacity {
			state.mu.Unlock()
			a.fastNanShareLog("fallback")
			return false
		}
		if state.flights == nil {
			state.flights = make(map[string]*fastNanShareFlight)
		}
		flight = &fastNanShareFlight{done: make(chan struct{})}
		state.flights[key] = flight
		request := r.Clone(context.WithoutCancel(r.Context()))
		go func() {
			result := resolveFastNanShareWithin(request, item.URL, wait)
			state.mu.Lock()
			defer state.mu.Unlock()
			flight.result = result
			now := time.Now()
			for cachedKey, entry := range state.cache {
				if !now.Before(entry.until) {
					delete(state.cache, cachedKey)
				}
			}
			if result.location != "" && now.Before(result.until) {
				if state.cache == nil {
					state.cache = make(map[string]fastNanShareResult)
				}
				if len(state.cache) >= fastNanShareCapacity {
					for cachedKey := range state.cache {
						delete(state.cache, cachedKey)
						break
					}
				}
				state.cache[key] = result
			}
			delete(state.flights, key)
			close(flight.done)
		}()
	}
	state.mu.Unlock()
	var continuationTimer <-chan time.Time
	if canContinue && continuation < 3 && wait > 5*time.Second {
		timer := time.NewTimer(5 * time.Second)
		defer timer.Stop()
		continuationTimer = timer.C
	}
	select {
	case <-r.Context().Done():
		return false
	case <-continuationTimer:
		if r.Context().Err() != nil {
			return false
		}
		writeFastContinuation(w, r, continuation)
		return true
	case <-flight.done:
		recordSourceRedirect(r, flight.result.source)
		if r.Context().Err() == nil && flight.result.location != "" {
			a.fastNanShareLog("source 302")
			writeFastNanShare(w, flight.result.location)
			return true
		}
		a.fastNanShareLog("fallback")
		return false
	}
}

func writeFastNanShare(w http.ResponseWriter, location string) {
	w.Header().Set("Location", location)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusFound)
}

func resolveFastNanShareWithin(r *http.Request, source string, wait time.Duration) fastNanShareResult {
	ctx, cancel := context.WithTimeout(r.Context(), wait)
	defer cancel()
	request := r.Clone(ctx)
	if request.Method == http.MethodHead {
		request.Method = http.MethodGet
	}
	result := fastNanShareResult{source: fetchSourceRedirect(request, fastSourceRequestURL(source))}
	contentType, _, _ := strings.Cut(result.source.contentType, ";")
	contentType = strings.ToLower(strings.TrimSpace(contentType))
	if result.source.status == http.StatusOK && (contentType == "application/json" || contentType == "text/html") {
		timer := time.NewTimer(100 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return result
		case <-timer.C:
			result.source = fetchSourceRedirect(request, fastSourceRequestURL(source))
		}
	}
	if !sourceRedirectStatus(result.source.status) || result.source.location == "" || strings.ContainsAny(result.source.location, "\r\n\t ") || !fastHTTPSource(result.source.location) {
		return result
	}
	location, _ := url.Parse(result.source.location)
	if sameSourceOrigin(source, location.String()) || strings.EqualFold(location.Host, r.Host) {
		return result
	}
	result.location = location.String()
	result.until = fastNanShareExpiry(location, time.Now())
	return result
}

func fastNanShareExpiry(location *url.URL, now time.Time) time.Time {
	until := now.Add(15 * time.Second)
	query := make(url.Values)
	for key, values := range location.Query() {
		key = strings.ToLower(key)
		query[key] = append(query[key], values...)
	}
	shorten := func(expiry time.Time) {
		expiry = expiry.Add(-5 * time.Second)
		if expiry.Before(until) {
			until = expiry
		}
	}
	for _, key := range []string{"expires", "expire", "expiry", "expiration", "exp", "e", "t", "x-oss-expires"} {
		for _, value := range query[key] {
			stamp, err := strconv.ParseInt(value, 10, 64)
			if err != nil || stamp <= 0 {
				return now
			}
			if stamp > 100000000000 {
				shorten(time.UnixMilli(stamp))
			} else {
				shorten(time.Unix(stamp, 0))
			}
		}
	}
	for _, prefix := range []string{"x-amz-", "x-goog-"} {
		values, present := query[prefix+"expires"]
		if !present {
			continue
		}
		dates := query[prefix+"date"]
		if len(dates) != 1 {
			return now
		}
		date, err := time.Parse("20060102T150405Z", dates[0])
		if err != nil {
			return now
		}
		for _, value := range values {
			seconds, err := strconv.ParseInt(value, 10, 64)
			if err != nil || seconds < 0 || seconds > 604800 {
				return now
			}
			shorten(date.Add(time.Duration(seconds) * time.Second))
		}
	}
	return until
}

func fastSourceRequestURL(source string) string {
	parsed, err := url.Parse(source)
	if err != nil {
		return source
	}
	const hex = "0123456789ABCDEF"
	var query strings.Builder
	for i := 0; i < len(parsed.RawQuery); i++ {
		b := parsed.RawQuery[i]
		if b >= '!' && b <= '~' {
			query.WriteByte(b)
		} else {
			query.WriteByte('%')
			query.WriteByte(hex[b>>4])
			query.WriteByte(hex[b&15])
		}
	}
	parsed.RawQuery = query.String()
	return parsed.String()
}

func fastHTTPSource(source string) bool {
	parsed, err := url.Parse(source)
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Hostname() != "" && parsed.User == nil && !embyVideoLocation(parsed.Path)
}
