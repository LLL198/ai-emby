package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

var xiaoyaClient = &http.Client{Timeout: 40 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}

// Resolve the original STRM, never the viewer-facing authenticated playback URL.
func xiaoyaSource(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	h := strings.ToLower(u.Hostname())
	return (h == "xiaoya.host" || h == "172.17.0.1" || h == "127.0.0.1" || h == "localhost") && u.Port() == "5678"
}

func xiaoyaLink(r *http.Request, raw, endpoint string) (string, error) {
	source, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid STRM")
	}
	target, err := url.Parse(endpoint)
	if err != nil || target.Host == "" || (target.Scheme != "http" && target.Scheme != "https") {
		return "", fmt.Errorf("invalid resolver address")
	}
	target.Path = strings.TrimRight(target.Path, "/") + source.Path
	target.RawPath = ""
	query := source.Query()
	if query.Get("sign") == "SIGN_STR" {
		query.Del("sign")
	}
	target.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, target.String(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", r.UserAgent())
	req.Header.Set("X-Alist-OriUA", r.UserAgent())
	res, err := xiaoyaClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("resolver request failed")
	}
	defer res.Body.Close()
	link := ""
	switch res.StatusCode {
	case 301, 302, 303, 307, 308:
		link = res.Header.Get("Location")
	case 200:
		b, err := io.ReadAll(io.LimitReader(res.Body, 65537))
		if err != nil || len(b) > 65536 {
			return "", fmt.Errorf("invalid resolver response")
		}
		link = strings.TrimSpace(string(b))
		if strings.HasPrefix(link, "{") {
			var v struct {
				URL string `json:"url"`
			}
			if json.Unmarshal(b, &v) != nil {
				return "", fmt.Errorf("invalid resolver JSON")
			}
			link = v.URL
		}
	default:
		return "", fmt.Errorf("resolver HTTP %d", res.StatusCode)
	}
	u, err := url.Parse(link)
	if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") || strings.ContainsAny(link, "\r\n") {
		return "", fmt.Errorf("resolver did not return an HTTP link")
	}
	h := strings.ToLower(u.Hostname())
	ip := net.ParseIP(h)
	if h == "localhost" || h == "xiaoya.host" || h == strings.ToLower(target.Hostname()) || (ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() || ip.IsLinkLocalUnicast())) {
		return "", fmt.Errorf("resolver returned an internal address")
	}
	return u.String(), nil
}

func (a *App) resolveXiaoya(w http.ResponseWriter, r *http.Request, x Item) {
	link, err := xiaoyaLink(r, x.URL, os.Getenv("XIAOYA_URL"))
	if err != nil {
		fail(w, http.StatusBadGateway, "Xiaoya 直链解析失败: "+err.Error())
		return
	}
	w.Header().Set("Location", link)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusFound)
}
