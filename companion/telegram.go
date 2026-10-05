package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

type telegramConfig struct {
	Enabled  bool   `json:"telegram_enabled"`
	Token    string `json:"telegram_token"`
	ChatID   string `json:"telegram_chat_id"`
	Notify   bool   `json:"telegram_notify_enabled"`
	NewMedia bool   `json:"telegram_notify_new_media"`
	Playback bool   `json:"telegram_notify_playback"`
}

type telegramState struct {
	mu       sync.Mutex
	configMu sync.Mutex
	running  bool
	wake     chan struct{}
	debounce int64
	queue    []notificationJob
	sessions map[string]time.Time
	client   *http.Client
}

var telegramTokenPattern = regexp.MustCompile(`^[0-9]+:[A-Za-z0-9_-]+$`)

func (a *App) telegramSettings() telegramConfig {
	var raw string
	var config telegramConfig
	if a.db.QueryRow("SELECT v FROM settings WHERE k='telegram'").Scan(&raw) == nil {
		_ = json.Unmarshal([]byte(raw), &config)
	}
	return config
}

func telegramPublicConfig(config telegramConfig) telegramConfig {
	if config.Token != "" {
		config.Token = "••••••••"
	}
	return config
}

func (a *App) telegramAdmin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodGet {
		respond(w, telegramPublicConfig(a.telegramSettings()))
		return
	}
	if r.Method != http.MethodPut {
		fail(w, 405, "PUT required")
		return
	}
	a.telegram.configMu.Lock()
	defer a.telegram.configMu.Unlock()
	config := a.telegramSettings()
	previous := config.Token
	if !body(w, r, &config) {
		return
	}
	config.Token, config.ChatID = strings.TrimSpace(config.Token), strings.TrimSpace(config.ChatID)
	if config.Token == "" || strings.ContainsAny(config.Token, "*•") {
		config.Token = previous
	}
	if len(config.Token) > 256 || len(config.ChatID) > 128 {
		fail(w, 400, "设置内容过长")
		return
	}
	if config.Token != "" && !telegramTokenPattern.MatchString(config.Token) {
		fail(w, 400, "Bot Token 格式不正确")
		return
	}
	raw, err := json.Marshal(config)
	if err == nil {
		_, err = a.db.Exec("INSERT INTO settings(k,v) VALUES('telegram',?) ON CONFLICT(k) DO UPDATE SET v=excluded.v", string(raw))
	}
	if err != nil {
		fail(w, 500, "保存设置失败")
		return
	}
	respond(w, telegramPublicConfig(config))
}

func (a *App) telegramTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		fail(w, 405, "POST required")
		return
	}
	if err := a.telegramRequest(a.telegramSettings(), "✅ AI Emby Telegram Bot 测试成功", "", nil); err != nil {
		a.telegramLog("Telegram 测试失败", err.Error())
		fail(w, 400, err.Error())
		return
	}
	a.telegramLog("Telegram 测试成功", "")
	respond(w, M{"ok": true, "Message": "OK"})
}

func (a *App) telegramRequest(config telegramConfig, text, image string, markup any) error {
	if !config.Enabled {
		return errors.New("Telegram Bot 未启用")
	}
	if config.Token == "" || config.ChatID == "" {
		return errors.New("请先设置 Bot Token 和 Chat ID")
	}
	if !telegramTokenPattern.MatchString(config.Token) || len(config.Token) > 256 || len(config.ChatID) > 128 {
		return errors.New("Telegram 请求配置错误")
	}
	values := url.Values{"parse_mode": {"HTML"}, "chat_id": {config.ChatID}, "text": {text}}
	if markup != nil {
		raw, err := json.Marshal(markup)
		if err != nil {
			return errors.New("Telegram 请求配置错误")
		}
		values.Set("reply_markup", string(raw))
	}
	method, contentType := "sendMessage", "application/x-www-form-urlencoded"
	var payload bytes.Buffer
	if image != "" {
		var data []byte
		remote := strings.HasPrefix(image, "https://") || strings.HasPrefix(image, "http://")
		if !remote {
			if strings.HasPrefix(image, "cover:") {
				_ = a.db.QueryRow("SELECT data FROM covers WHERE id=?", strings.TrimPrefix(image, "cover:")).Scan(&data)
			} else if path := safeImage(image); path != "" {
				if file, err := os.Open(path); err == nil {
					data, _ = io.ReadAll(io.LimitReader(file, (10<<20)+1))
					_ = file.Close()
				}
			}
			if len(data) == 0 || len(data) > 10<<20 {
				image = ""
			}
		}
		if image != "" {
			method = "sendPhoto"
			values.Del("text")
			values.Set("caption", text)
			if remote {
				values.Set("photo", image)
			} else {
				writer := multipart.NewWriter(&payload)
				for key, entries := range values {
					if err := writer.WriteField(key, entries[0]); err != nil {
						return errors.New("Telegram 请求配置错误")
					}
				}
				file, err := writer.CreateFormFile("photo", "poster.jpg")
				if err == nil {
					_, err = file.Write(data)
				}
				if err == nil {
					err = writer.Close()
				}
				if err != nil {
					return errors.New("Telegram 请求配置错误")
				}
				contentType = writer.FormDataContentType()
			}
		}
	}
	if payload.Len() == 0 {
		payload.WriteString(values.Encode())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.telegram.org/bot"+config.Token+"/"+method, &payload)
	if err != nil {
		return errors.New("Telegram 请求配置错误")
	}
	request.Header.Set("Content-Type", contentType)
	a.telegram.mu.Lock()
	client := a.telegram.client
	if client == nil {
		client = &http.Client{Timeout: 4 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		a.telegram.client = client
	}
	a.telegram.mu.Unlock()
	response, err := client.Do(request)
	if err != nil {
		return errors.New("Telegram 连接失败或超时")
	}
	defer response.Body.Close()
	var result struct {
		OK bool `json:"ok"`
	}
	if response.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&result) != nil || !result.OK {
		return errors.New("Telegram 拒绝发送，请检查 Token、Chat ID 和机器人权限")
	}
	return nil
}

func telegramEventEnabled(config telegramConfig, event NotifyEvent) bool {
	return config.Enabled && config.Notify && ((event.Type == "new-media" && config.NewMedia) || (event.Type == "playback" && config.Playback))
}

func (a *App) sendTelegram(event NotifyEvent) error {
	config := a.telegramSettings()
	if !telegramEventEnabled(config, event) {
		return nil
	}
	text, markup := telegramMessage(event)
	return a.telegramRequest(config, text, event.ImageURL, markup)
}
