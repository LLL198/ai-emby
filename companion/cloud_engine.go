package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

const cloudEngineURL = "http://127.0.0.1:18099"

var cloudClient = struct {
	sync.Mutex
	token   string
	expires time.Time
}{}
var cloudManageMu sync.Mutex
var cloudHTTP = &http.Client{Timeout: 90 * time.Second}
var errCloudEngine = errors.New("网盘引擎暂时不可用，请稍后重试")
var errCloudAccount = errors.New("网盘操作失败，请检查账号是否过期、目录权限和网盘登录配置")

type cloudEnvelope struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

func cloudCall(ctx context.Context, method, endpoint string, payload any, result any, ua string) error {
	cloudClient.Lock()
	if cloudClient.token == "" || time.Now().After(cloudClient.expires) {
		data, err := os.ReadFile("/app/data/cloud-engine/credentials.json")
		if err != nil {
			cloudClient.Unlock()
			return errCloudEngine
		}
		req, err := http.NewRequestWithContext(ctx, "POST", cloudEngineURL+"/api/auth/login", bytes.NewReader(data))
		if err != nil {
			cloudClient.Unlock()
			return errCloudEngine
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := cloudHTTP.Do(req)
		if err != nil {
			cloudClient.Unlock()
			return errCloudEngine
		}
		var envelope cloudEnvelope
		err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&envelope)
		resp.Body.Close()
		var login struct {
			Token string `json:"token"`
		}
		if err != nil || envelope.Code != 200 || json.Unmarshal(envelope.Data, &login) != nil || login.Token == "" {
			cloudClient.Unlock()
			return errCloudEngine
		}
		cloudClient.token, cloudClient.expires = login.Token, time.Now().Add(24*time.Hour)
	}
	token := cloudClient.token
	cloudClient.Unlock()
	var data []byte
	if payload != nil {
		var err error
		data, err = json.Marshal(payload)
		if err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, cloudEngineURL+endpoint, bytes.NewReader(data))
	if err != nil {
		return errCloudEngine
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", token)
	req.Header.Set("User-Agent", ua)
	resp, err := cloudHTTP.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errCloudEngine
	}
	defer resp.Body.Close()
	var envelope cloudEnvelope
	if json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&envelope) != nil {
		return errCloudEngine
	}
	if envelope.Code == 401 || resp.StatusCode == 401 {
		cloudClient.Lock()
		cloudClient.token = ""
		cloudClient.Unlock()
		return errCloudEngine
	}
	// Engine messages can contain provider credentials or signed URLs.
	if result != nil && len(envelope.Data) > 0 && string(envelope.Data) != "null" {
		if err := json.Unmarshal(envelope.Data, result); err != nil {
			return errCloudEngine
		}
	}
	if envelope.Code != 200 || resp.StatusCode != 200 {
		if strings.Contains(strings.ToLower(envelope.Message), "plf_invalid") {
			return errors.New("夸克转码直链接口拒绝了当前请求（plf_invalid）。可在网盘配置中选择服务器中转；302 播放需要移动端接口凭据")
		}
		return errCloudAccount
	}
	return nil
}

type cloudField struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Default  string `json:"default"`
	Options  string `json:"options"`
	Required bool   `json:"required"`
}
type cloudDriver struct {
	Additional []cloudField `json:"additional"`
}

var cloudDrivers = map[string]string{"139Yun": "移动云盘", "115 Cloud": "115 云盘", "115 Open": "115 开放平台", "Quark": "夸克网盘", "GuangYaPan": "光鸭云盘", "WebDav": "WebDAV"}

func cloudSensitive(key string) bool {
	key = strings.ToLower(key)
	for _, part := range []string{"token", "cookie", "password", "authorization", "verify_code", "sms_code", "device_sign", "verification_id", "username", "phone_number"} {
		if strings.Contains(key, part) {
			return true
		}
	}
	return false
}

type cloudStorage struct {
	ID              int64  `json:"id"`
	MountPath       string `json:"mount_path"`
	Driver          string `json:"driver"`
	Addition        string `json:"addition"`
	Status          string `json:"status"`
	Disabled        bool   `json:"disabled"`
	CacheExpiration int    `json:"cache_expiration"`
	WebProxy        bool   `json:"web_proxy"`
	WebdavPolicy    string `json:"webdav_policy"`
	EnableSign      bool   `json:"enable_sign"`
}

func cloudGetStorage(ctx context.Context, sid int64) (cloudStorage, error) {
	var s cloudStorage
	err := cloudCall(ctx, "GET", "/api/admin/storage/get?id="+cloudInt(sid), nil, &s, "")
	return s, err
}
