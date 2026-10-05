package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"syscall"
	"time"
)

const maxScraperArtworkConcurrency = 6

var scraperArtworkSlots = make(chan struct{}, maxScraperArtworkConcurrency)

// Wait before starting the per-target timeout, so a large media batch does not
// use up download time while queued. The task context still cancels the wait.
func acquireScraperArtwork(ctx context.Context) (func(), bool) {
	if ctx.Err() != nil {
		return nil, false
	}
	select {
	case scraperArtworkSlots <- struct{}{}:
		return func() { <-scraperArtworkSlots }, true
	case <-ctx.Done():
		return nil, false
	}
}

func scraperArtworkAttempt(ctx context.Context, client *http.Client, rawURL string) ([]byte, bool, time.Duration, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, false, 0, errors.New("图片请求无效")
	}
	response, err := client.Do(request)
	if err != nil {
		var network net.Error
		retry := errors.As(err, &network) && (network.Timeout() || network.Temporary()) ||
			errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
			errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNREFUSED)
		return nil, retry, 0, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		code := response.StatusCode
		retry := code == http.StatusRequestTimeout || code == http.StatusTooManyRequests || code >= 500 && code <= 599
		delay := time.Duration(0)
		if seconds, err := strconv.Atoi(response.Header.Get("Retry-After")); err == nil && seconds > 0 {
			delay = time.Duration(min(seconds, 3)) * time.Second
		}
		return nil, retry, delay, fmt.Errorf("图片 HTTP %d", code)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxArtworkBytes+1))
	if err != nil {
		return nil, true, 0, fmt.Errorf("图片读取失败：%w", err)
	}
	if len(data) > maxArtworkBytes {
		return nil, false, 0, errors.New("图片超过20MB")
	}
	if err := scraperValidateImage(data); err != nil {
		return nil, false, 0, err
	}
	return data, false, 0, nil
}

func (a *App) downloadScraperArtwork(ctx context.Context, rawURL string) ([]byte, error) {
	client := a.externalHTTPClient("tmdb", posterHTTP)
	for attempt := 1; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		data, retry, delay, err := scraperArtworkAttempt(ctx, client, rawURL)
		if err == nil {
			return data, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if !retry || attempt == 3 {
			message := scraperSanitize(err.Error(), "")
			if a != nil && a.db != nil {
				message = a.scraperSafeError(err).Error()
			}
			return nil, fmt.Errorf("图片下载失败（已尝试 %d 次）：%s", attempt, message)
		}
		delay = max(delay, time.Duration(attempt)*250*time.Millisecond)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}
