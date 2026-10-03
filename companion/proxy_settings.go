package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type proxySettingsRequest struct {
	Enabled                       bool
	Type, URL, Username, Password string
	ClearPassword                 bool
	Scopes                        map[string]bool
}

func defaultProxyScopes() map[string]bool {
	return map[string]bool{"tmdb": true, "subtitle": true, "update": true, "generic": false}
}

func (a *App) loadProxySettings() {
	config := proxySettings{Type: "HTTP", Scopes: defaultProxyScopes()}
	var stored string
	if a.db.QueryRow("SELECT v FROM settings WHERE k='external_proxy_settings'").Scan(&stored) == nil {
		_ = json.Unmarshal([]byte(stored), &config)
	}
	_ = a.db.QueryRow("SELECT v FROM settings WHERE k='external_proxy_password'").Scan(&config.Password)
	config.PasswordConfigured = config.Password != ""
	config.Scopes = normalizeProxyScopes(config.Scopes)
	a.proxy.replace(config)
}

func normalizeProxyScopes(scopes map[string]bool) map[string]bool {
	normalized := defaultProxyScopes()
	for scope := range normalized {
		if enabled, ok := scopes[scope]; ok {
			normalized[scope] = enabled
		}
	}
	return normalized
}

func validateProxy(config proxySettings) error {
	if config.Type != "HTTP" && config.Type != "HTTPS" && config.Type != "SOCKS5" {
		return errInvalidProxyConfiguration
	}
	if !config.Enabled && strings.TrimSpace(config.URL) == "" {
		return nil
	}
	parsed, err := url.Parse(config.URL)
	if err != nil || parsed.Hostname() == "" || parsed.Port() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != "" && parsed.Path != "/" || parsed.Scheme != strings.ToLower(config.Type) {
		return errInvalidProxyConfiguration
	}
	for scope := range config.Scopes {
		switch scope {
		case "tmdb", "subtitle", "update", "generic":
		default:
			return errInvalidProxyConfiguration
		}
	}
	return nil
}

func (a *App) proxySettingsAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		respond(w, a.proxy.snapshot())
		return
	}
	if r.Method != http.MethodPut {
		fail(w, http.StatusMethodNotAllowed, "GET/PUT required")
		return
	}
	var request proxySettingsRequest
	if !body(w, r, &request) {
		return
	}
	previous := a.proxy.snapshot()
	config := proxySettings{Enabled: request.Enabled, Type: request.Type, URL: strings.TrimSpace(request.URL), Username: request.Username, Password: previous.Password, Scopes: request.Scopes}
	if config.Scopes == nil {
		config.Scopes = defaultProxyScopes()
	}
	if request.ClearPassword {
		config.Password = ""
	} else if request.Password != "" {
		config.Password = request.Password
	}
	if validateProxy(config) != nil {
		fail(w, http.StatusBadRequest, "代理类型、地址或范围无效")
		return
	}
	data, err := json.Marshal(config)
	if err != nil {
		fail(w, http.StatusInternalServerError, "保存失败")
		return
	}
	tx, err := a.db.Begin()
	if err != nil {
		fail(w, http.StatusInternalServerError, "保存失败")
		return
	}
	defer tx.Rollback()
	_, err = tx.Exec("INSERT INTO settings(k,v) VALUES('external_proxy_settings',?) ON CONFLICT(k) DO UPDATE SET v=excluded.v", string(data))
	if err == nil {
		_, err = tx.Exec("INSERT INTO settings(k,v) VALUES('external_proxy_password',?) ON CONFLICT(k) DO UPDATE SET v=excluded.v", config.Password)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "保存失败")
		return
	}
	config.PasswordConfigured = config.Password != ""
	a.proxy.replace(config)
	respond(w, a.proxy.snapshot())
}

func (a *App) proxyTestAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		fail(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	var request proxySettingsRequest
	if !body(w, r, &request) {
		return
	}
	config := proxySettings{Enabled: true, Type: request.Type, URL: strings.TrimSpace(request.URL), Username: request.Username, Password: a.proxy.snapshot().Password}
	if request.ClearPassword {
		config.Password = ""
	} else if request.Password != "" {
		config.Password = request.Password
	}
	if validateProxy(config) != nil {
		fail(w, http.StatusBadRequest, "代理类型或地址无效")
		return
	}
	started := time.Now()
	client, cleanup, err := newExternalClient(&http.Client{Timeout: 5 * time.Second}, config)
	if err != nil {
		fail(w, http.StatusBadRequest, "代理配置无效")
		return
	}
	defer cleanup()
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	probe, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://www.gstatic.com/generate_204", nil)
	if err != nil {
		fail(w, http.StatusBadRequest, "代理配置无效")
		return
	}
	response, err := client.Do(probe)
	if err != nil {
		message := "连接失败：代理或目标不可达"
		if ctx.Err() != nil {
			message = "连接超时（5 秒）"
		}
		fail(w, http.StatusBadGateway, message)
		return
	}
	response.Body.Close()
	respond(w, M{"Success": true, "LatencyMs": time.Since(started).Milliseconds()})
}
