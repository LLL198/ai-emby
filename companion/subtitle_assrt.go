package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var subtitleHTTPClient = &http.Client{
	Timeout: 20 * time.Second,
	CheckRedirect: func(r *http.Request, previous []*http.Request) error {
		if len(previous) >= 10 || !subtitleOfficialURL(r.URL.String()) {
			return errors.New("非官方字幕地址")
		}
		return nil
	},
}

func subtitleOfficialURL(address string) bool {
	u, err := url.Parse(address)
	if err != nil || u.User != nil || u.Port() != "" || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host == "assrt.net" || strings.HasSuffix(host, ".assrt.net")
}

func (a *App) subtitleRequest(ctx context.Context, address, token string, limit int64) ([]byte, error) {
	if !subtitleOfficialURL(address) {
		return nil, errors.New("非官方字幕地址")
	}
	if limit < 1 || limit > subtitleMaxBytes {
		return nil, errors.New("字幕响应大小限制无效")
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, errors.New("字幕请求无效")
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	a.subtitles.mu.Lock()
	client := a.subtitles.client
	a.subtitles.mu.Unlock()
	if client == nil {
		client = a.externalHTTPClient("subtitle", subtitleHTTPClient)
	}
	response, err := client.Do(r)
	if err != nil {
		return nil, errors.New("字幕网络请求失败")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("字幕 HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, errors.New("字幕响应读取失败或超限")
	}
	return data, nil
}

func (a *App) assrtAPI(ctx context.Context, method string, query url.Values, token string) ([]assrtSub, error) {
	if method != "search" && method != "detail" {
		return nil, errors.New("字幕请求无效")
	}
	a.subtitles.mu.Lock()
	if a.subtitles.client == nil {
		at := a.subtitles.nextAPI
		if now := time.Now(); at.Before(now) {
			at = now
		}
		a.subtitles.nextAPI = at.Add(3100 * time.Millisecond)
		a.subtitles.mu.Unlock()
		timer := time.NewTimer(time.Until(at))
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
		}
	} else {
		a.subtitles.mu.Unlock()
	}
	data, err := a.subtitleRequest(ctx, "https://api.assrt.net/v1/sub/"+method+"?"+query.Encode(), token, 2<<20)
	if err != nil {
		return nil, err
	}
	var response struct {
		Status *int `json:"status"`
		Sub    struct {
			Subs json.RawMessage `json:"subs"`
		} `json:"sub"`
	}
	if json.Unmarshal(data, &response) != nil || response.Status == nil {
		return nil, errors.New("字幕 API 响应无效")
	}
	if *response.Status != 0 {
		return nil, fmt.Errorf("字幕 API 状态 %d", *response.Status)
	}
	raw := strings.TrimSpace(string(response.Sub.Subs))
	if raw == "{}" || raw == "[]" || raw == "null" {
		return nil, nil
	}
	var candidates []assrtSub
	if raw == "" || json.Unmarshal(response.Sub.Subs, &candidates) != nil {
		return nil, errors.New("字幕 API 候选列表无效")
	}
	return candidates, nil
}
