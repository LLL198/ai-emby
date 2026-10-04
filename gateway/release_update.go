package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
)

const updateRepository = "LLL198/ai-emby"

var releaseVersion = "development"
var updateMu sync.Mutex
var releaseSHA256 = regexp.MustCompile(`^[a-f0-9]{64}$`)

func currentVersion() string {
	if data, err := os.ReadFile("/app/VERSION"); err == nil {
		version := strings.TrimSpace(string(data))
		if known, _ := compareReleaseVersions(version, version); known {
			return version
		}
	}
	return releaseVersion
}

func updateControlRoot() string {
	if root := os.Getenv("UPDATE_CONTROL_ROOT"); root != "" {
		return root
	}
	return "/app/update-control"
}

type releaseInfo struct {
	Tag        string `json:"tag_name"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
	Assets     []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

type updateManifest struct {
	Version, Repository, Architecture, Asset, SHA256 string
}

type updateRequest struct {
	Version, Repository, ManifestURL, Proxy string
	RequestedAt                             time.Time
}

func updateProxy(ctx context.Context, db *sql.DB) (string, error) {
	if db == nil {
		return "", fmt.Errorf("更新配置暂时不可用")
	}
	var raw string
	err := db.QueryRowContext(ctx, "SELECT v FROM settings WHERE k='external_proxy_settings'").Scan(&raw)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("读取更新代理失败")
	}
	var config struct {
		Enabled       bool
		URL, Username string
		Scopes        map[string]bool
	}
	if json.Unmarshal([]byte(raw), &config) != nil {
		return "", fmt.Errorf("更新代理配置无效")
	}
	if !config.Enabled || !config.Scopes["update"] {
		return "", nil
	}
	parsed, err := url.Parse(config.URL)
	if err != nil || parsed.Hostname() == "" {
		return "", fmt.Errorf("更新代理地址无效")
	}
	if config.Username != "" {
		var password string
		err = db.QueryRowContext(ctx, "SELECT v FROM settings WHERE k='external_proxy_password'").Scan(&password)
		if err != nil && err != sql.ErrNoRows {
			return "", fmt.Errorf("读取更新代理失败")
		}
		parsed.User = url.UserPassword(config.Username, password)
	}
	return parsed.String(), nil
}

func updateHTTPJSON(ctx context.Context, client *http.Client, endpoint string, out any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "ai-emby/"+currentVersion())
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("无法连接新仓库的更新服务，请检查网络或更新代理")
	}
	defer response.Body.Close()
	if response.StatusCode == 404 {
		return fmt.Errorf("新仓库尚未发布更新包")
	}
	if response.StatusCode != 200 {
		return fmt.Errorf("更新服务器返回 HTTP %d", response.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(out); err != nil {
		return fmt.Errorf("更新服务器返回无效数据")
	}
	return nil
}

func (checker *releaseChecker) fetch(ctx context.Context, proxy string) (releaseInfo, updateManifest, string, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	defer transport.CloseIdleConnections()
	if proxy != "" {
		parsed, err := url.Parse(proxy)
		if err != nil {
			return releaseInfo{}, updateManifest{}, "", fmt.Errorf("更新代理无效")
		}
		transport.Proxy = http.ProxyURL(parsed)
	}
	client := &http.Client{Transport: transport, Timeout: 12 * time.Second}
	var release releaseInfo
	var manifest updateManifest
	err := updateHTTPJSON(ctx, client, "https://api.github.com/repos/"+updateRepository+"/releases/latest", &release)
	if err != nil {
		return release, manifest, "", err
	}
	versionKnown, _ := compareReleaseVersions(release.Tag, release.Tag)
	if release.Draft || release.Prerelease || !versionKnown {
		return release, manifest, "", fmt.Errorf("发布版本无效")
	}
	assetName := "ai-emby-linux-" + runtime.GOARCH + ".tar.gz"
	prefix := "https://github.com/" + updateRepository + "/releases/download/" + release.Tag + "/"
	manifestURL, bundleURL := "", ""
	for _, asset := range release.Assets {
		if asset.Name == "update-linux-"+runtime.GOARCH+".json" && asset.URL == prefix+asset.Name {
			manifestURL = asset.URL
		}
		if asset.Name == assetName && asset.URL == prefix+asset.Name {
			bundleURL = asset.URL
		}
	}
	if manifestURL == "" || bundleURL == "" {
		return release, manifest, "", fmt.Errorf("发布版本缺少本机架构的更新包")
	}
	if err = updateHTTPJSON(ctx, client, manifestURL, &manifest); err != nil {
		return release, manifest, "", err
	}
	if manifest.Repository != updateRepository || manifest.Version != release.Tag || manifest.Architecture != runtime.GOARCH || manifest.Asset != assetName || !releaseSHA256.MatchString(manifest.SHA256) {
		return release, manifest, "", fmt.Errorf("更新包清单校验失败")
	}
	return release, manifest, manifestURL, nil
}

func updateEnabled() bool {
	var ready struct {
		Repository string
		Time       time.Time
	}
	data, err := os.ReadFile(filepath.Join(updateControlRoot(), "ready.json"))
	return err == nil && json.Unmarshal(data, &ready) == nil && ready.Repository == updateRepository && time.Since(ready.Time) < time.Minute && time.Since(ready.Time) >= -time.Minute
}

func serveReleaseUpdates(db *sql.DB, w http.ResponseWriter, r *http.Request) bool {
	path := strings.TrimPrefix(r.URL.Path, "/emby")
	if path != "/System/Updates" && path != "/System/Updates/Status" {
		return false
	}
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet && (path != "/System/Updates" || r.Method != http.MethodPost) {
		sessionError(w, 405, "GET/POST required")
		return true
	}
	// Validate administrator access before accepting update requests.
	authorized, err := get(r, coreURL, "/admin/library-settings")
	if err != nil {
		sessionError(w, 502, "核心服务暂时不可用")
		return true
	}
	if authorized.StatusCode != 200 {
		relay(w, authorized)
		return true
	}
	authorized.Body.Close()
	if path == "/System/Updates/Status" {
		status := map[string]any{"State": "idle", "CurrentVersion": currentVersion(), "Repository": updateRepository, "Enabled": updateEnabled()}
		data, err := os.ReadFile(filepath.Join(updateControlRoot(), "status.json"))
		if err == nil {
			_ = json.Unmarshal(data, &status)
		}
		status["CurrentVersion"], status["Repository"], status["Enabled"] = currentVersion(), updateRepository, updateEnabled()
		scanRespond(w, status)
		return true
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	proxy, err := updateProxy(ctx, db)
	if err != nil {
		sessionError(w, 503, err.Error())
		return true
	}
	release, _, manifestURL, err := updates.latest(ctx, proxy)
	if err != nil {
		sessionError(w, 502, err.Error())
		return true
	}
	known, newer := compareReleaseVersions(currentVersion(), release.Tag)
	if r.Method == http.MethodGet {
		scanRespond(w, map[string]any{"CurrentVersion": currentVersion(), "LatestVersion": release.Tag, "VersionKnown": known, "UpdateAvailable": !known || newer, "Repository": updateRepository, "Enabled": updateEnabled()})
		return true
	}
	if !updateEnabled() {
		sessionError(w, 503, "宿主机更新服务未启动，请先运行仓库 scripts/install-updater.sh")
		return true
	}
	if known && !newer {
		sessionError(w, 409, "已是最新版本")
		return true
	}
	updateMu.Lock()
	defer updateMu.Unlock()
	root := updateControlRoot()
	if _, err := os.Stat(filepath.Join(root, "processing.json")); err == nil {
		sessionError(w, 409, "更新任务正在进行")
		return true
	}
	requestedAt := time.Now().UTC()
	requestData, _ := json.Marshal(updateRequest{release.Tag, updateRepository, manifestURL, proxy, requestedAt})
	tmp, err := os.CreateTemp(root, ".request-")
	if err != nil {
		sessionError(w, 503, "无法创建更新任务")
		return true
	}
	name := tmp.Name()
	defer os.Remove(name)
	_, writeErr := tmp.Write(requestData)
	closeErr := tmp.Close()
	if writeErr != nil || closeErr != nil {
		sessionError(w, 503, "无法保存更新任务")
		return true
	}
	if err := os.Link(name, filepath.Join(root, "request.json")); err != nil {
		sessionError(w, 409, "已有更新任务或更新目录不可写")
		return true
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusAccepted)
	scanRespond(w, map[string]any{"State": "queued", "TargetVersion": release.Tag, "Repository": updateRepository, "RequestedAt": requestedAt})
	return true
}
