package main

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
)

var errInvalidProxyConfiguration = errors.New("invalid proxy configuration")

// Reconstructed from proxy_client.go at 0x7f6200-0x7f6fa0. Settings loading
// and the proxy administration routes are in proxy_settings.go.
func (state *proxyState) snapshot() proxySettings {
	state.mu.RLock()
	defer state.mu.RUnlock()
	config := state.settings
	config.Scopes = make(map[string]bool, len(state.settings.Scopes))
	for scope, enabled := range state.settings.Scopes {
		config.Scopes[scope] = enabled
	}
	return config
}

func (state *proxyState) replace(config proxySettings) {
	state.mu.Lock()
	oldTransports := state.transports
	state.settings = config
	state.clients = make(map[string]*http.Client)
	state.transports = nil
	state.mu.Unlock()
	for _, transport := range oldTransports {
		transport.CloseIdleConnections()
	}
}

func newExternalClient(base *http.Client, config proxySettings) (*http.Client, func(), error) {
	if !config.Enabled {
		return base, func() {}, nil
	}
	proxyURL, err := url.Parse(config.URL)
	if err != nil {
		return nil, nil, errInvalidProxyConfiguration
	}
	if config.Username != "" {
		if config.Password != "" {
			proxyURL.User = url.UserPassword(config.Username, config.Password)
		} else {
			proxyURL.User = url.User(config.Username)
		}
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if baseTransport, ok := base.Transport.(*http.Transport); ok {
		transport = baseTransport.Clone()
	}
	transport.Proxy = http.ProxyURL(proxyURL)
	client := *base
	client.Transport = transport
	return &client, transport.CloseIdleConnections, nil
}

func (a *App) externalHTTPClient(scope string, base *http.Client) *http.Client {
	if a == nil || a.appState == nil {
		return base
	}
	state := &a.proxy
	state.mu.RLock()
	if !state.settings.Enabled || !state.settings.Scopes[scope] {
		state.mu.RUnlock()
		return base
	}
	key := scope + ":" + fmt.Sprintf("%p", base)
	if client := state.clients[key]; client != nil {
		state.mu.RUnlock()
		return client
	}
	state.mu.RUnlock()
	state.mu.Lock()
	defer state.mu.Unlock()
	if !state.settings.Enabled || !state.settings.Scopes[scope] {
		return base
	}
	if client := state.clients[key]; client != nil {
		return client
	}
	client, _, err := newExternalClient(base, state.settings)
	if err != nil {
		return base
	}
	// The binary assumes replace/load initialized the client map.
	state.clients[key] = client
	if transport, ok := client.Transport.(*http.Transport); ok {
		state.transports = append(state.transports, transport)
	}
	return client
}
