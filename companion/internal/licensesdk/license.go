// Package licensesdk provides HTTPS authorization bound to the host machine ID.
package licensesdk

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

const DefaultServer = "https://tl.macacaaca.top"
const RefreshInterval = 12 * time.Hour
const MaxLeaseSeconds = 43260

type Client struct {
	OnError           func(string)
	mu                sync.RWMutex
	http              *http.Client
	url, code, device string
	until             time.Time
	reason, mode      string
}
type response struct {
	Valid  bool   `json:"valid"`
	Mode   string `json:"mode"`
	Reason string `json:"reason"`
	Lease  int    `json:"lease_seconds"`
}

func NewFromEnv() (*Client, error) {
	machineIDFile := os.Getenv("LICENSE_MACHINE_ID_FILE")
	if machineIDFile == "" {
		machineIDFile = "/run/license-machine-id"
	}
	raw, err := os.ReadFile(machineIDFile)
	if err != nil {
		return nil, fmt.Errorf("read host machine-id: %w", err)
	}
	id := strings.TrimSpace(string(raw))
	if len(id) != 32 {
		return nil, fmt.Errorf("host machine-id must contain 32 hex characters")
	}
	if _, err = hex.DecodeString(id); err != nil {
		return nil, fmt.Errorf("invalid host machine-id")
	}
	digest := sha256.Sum256([]byte("go-emby-device-v1:" + strings.ToLower(id)))
	roots, err := x509.SystemCertPool()
	if err != nil {
		return nil, fmt.Errorf("load system trust store: %w", err)
	}
	if caFile := os.Getenv("LICENSE_CA_FILE"); caFile != "" {
		certificate, err := os.ReadFile(caFile)
		if err != nil {
			return nil, fmt.Errorf("read LICENSE_CA_FILE: %w", err)
		}
		if !roots.AppendCertsFromPEM(certificate) {
			return nil, fmt.Errorf("invalid LICENSE_CA_FILE certificate")
		}
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}, MaxIdleConnsPerHost: 2, ResponseHeaderTimeout: 5 * time.Second}
	endpoint := strings.TrimRight(strings.TrimSpace(os.Getenv("LICENSE_SERVER_URL")), "/")
	if endpoint == "" {
		endpoint = DefaultServer
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("LICENSE_SERVER_URL must be an HTTPS URL without credentials, query or fragment")
	}
	return &Client{http: &http.Client{Timeout: 8 * time.Second, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, url: endpoint, code: strings.TrimSpace(os.Getenv("LICENSE_KEY")), device: hex.EncodeToString(digest[:]), reason: "not_verified"}, nil
}

func (c *Client) Refresh(ctx context.Context) {
	payload, _ := json.Marshal(map[string]any{"license": c.code, "device": c.device, "lease_seconds": MaxLeaseSeconds})
	started := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url+"/v1/check", bytes.NewReader(payload))
	if err != nil {
		c.networkError("request construction: " + err.Error())
		return
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := c.http.Do(req)
	if err != nil {
		detail := err.Error()
		if u, ok := err.(*url.Error); ok {
			detail = u.Err.Error()
		}
		c.networkError("HTTPS connection failed: " + detail)
		return
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		c.networkError(fmt.Sprintf("HTTP status %d", res.StatusCode))
		return
	}
	var result response
	if err = json.NewDecoder(io.LimitReader(res.Body, 4096)).Decode(&result); err != nil {
		c.networkError("invalid JSON response: " + err.Error())
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !result.Valid {
		c.until = time.Time{}
		c.reason = result.Reason
		c.mode = ""
		if c.reason == "" {
			c.reason = "denied"
		}
		c.reportError("authorization denied: " + c.reason)
		return
	}
	if result.Lease <= 0 || result.Lease > MaxLeaseSeconds {
		c.until = time.Time{}
		c.reason = "invalid_lease"
		c.reportError(c.reason)
		return
	}
	c.until = started.Add(time.Duration(result.Lease) * time.Second)
	c.reason = ""
	c.mode = result.Mode
}
func (c *Client) reportError(detail string) {
	log.Printf("license error: %s", detail)
	if c.OnError != nil {
		c.OnError(detail)
	}
}
func (c *Client) networkError(detail string) {
	c.reportError(detail)
	c.mu.Lock()
	defer c.mu.Unlock()
	if !time.Now().Before(c.until) {
		c.reason = "license_server_unavailable"
	}
}
func (c *Client) Status() (bool, string, string) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if time.Now().Before(c.until) {
		return true, c.mode, ""
	}
	reason := c.reason
	if reason == "" {
		reason = "lease_expired"
	}
	return false, c.mode, reason
}
func (c *Client) Run(ctx context.Context) {
	ticker := time.NewTicker(RefreshInterval)
	defer ticker.Stop()
	previous := ""
	for {
		valid, mode, reason := c.Status()
		state := fmt.Sprintf("valid=%t mode=%s reason=%s", valid, mode, reason)
		if state != previous {
			log.Printf("license: %s", state)
			previous = state
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.Refresh(ctx)
		}
	}
}
func (c *Client) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		valid, mode, reason := c.Status()
		if r.URL.Path == "/license/status" {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			json.NewEncoder(w).Encode(map[string]any{"valid": valid, "mode": mode, "reason": reason})
			return
		}
		if r.URL.Path == "/health" {
			next.ServeHTTP(w, r)
			return
		}
		if !valid {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusPaymentRequired)
			json.NewEncoder(w).Encode(map[string]string{"error": "license_required", "reason": reason, "message": "请在 Compose 环境变量 LICENSE_KEY 中配置有效授权码"})
			return
		}
		next.ServeHTTP(w, r)
	})
}
