package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/LLL198/ai-emby/security"
)

// Only the authenticated playback handler can create this context value.
// No URL parameter or HTTP header grants this authority.
type cloudPlaybackItemKey struct{}

type cloudPlaybackResponse struct {
	http.ResponseWriter
	wrote   bool
	onStart func()
}

func (w *cloudPlaybackResponse) WriteHeader(status int) {
	if status >= 100 && status < 200 {
		w.ResponseWriter.WriteHeader(status)
		return
	}
	if w.wrote {
		return
	}
	w.wrote = true
	w.ResponseWriter.WriteHeader(status)
	if status == http.StatusOK || status == http.StatusPartialContent {
		w.onStart()
	}
}

func (w *cloudPlaybackResponse) Write(data []byte) (int, error) {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(data)
}

func (w *cloudPlaybackResponse) Unwrap() http.ResponseWriter { return w.ResponseWriter }

const cloudInternalLifetime = 24 * time.Hour
const cloudInternalEndpoint = "http://127.0.0.1:18098"

// Read on every request so both processes see changes immediately. Errors must
// not silently turn protection off, as the general settings reader would do.
func (a *App) cloudPlaybackProtection(ctx context.Context) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	c, err := security.Load(ctx, a.db.DB)
	return c.ProtectCloudPlayback, err
}

func cloudSourceURL(raw string) (*url.URL, string, string, bool) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || !strings.HasPrefix(u.Path, "/cloud/resolve/") {
		return nil, "", "", false
	}
	id := strings.TrimPrefix(u.Path, "/cloud/resolve/")
	if id == "" || strings.Contains(id, "/") {
		return nil, "", "", false
	}
	q := u.Query()
	if len(q["path"]) != 1 || len(q["sign"]) != 1 {
		return nil, "", "", false
	}
	p, err := cloudPath(q.Get("path"))
	if err != nil || p == "/" {
		return nil, "", "", false
	}
	return u, id, p, true
}

func validCloudSourceSign(m cloudMount, p, sign string) bool {
	provided, err := hex.DecodeString(sign)
	expected, _ := hex.DecodeString(cloudSign(m, p))
	return err == nil && hmac.Equal(provided, expected)
}

// Resolve inside the authenticated handler. The permanent STRM URL and the
// internal task credential never leave the server, including in proxy mode.
func (a *App) protectedCloudStream(w http.ResponseWriter, r *http.Request, x Item) bool {
	u, _, _, ok := cloudSourceURL(x.URL)
	if !ok {
		return false
	}
	enabled, err := a.cloudPlaybackProtection(r.Context())
	if err != nil {
		fail(w, 503, "播放保护设置暂时不可用，请重试")
		return true
	}
	if !enabled {
		return false
	}
	request := r.Clone(context.WithValue(r.Context(), cloudPlaybackItemKey{}, x.ID))
	request.Header.Set("X-Emby-Token", token(r))
	request.URL = u
	w.Header().Set("X-AI-Emby-Protected-Playback", "true")
	// A proxy stream can remain open for hours; start next-episode preparation
	// when its successful headers are sent instead of waiting for it to finish.
	response := &cloudPlaybackResponse{ResponseWriter: w, onStart: func() { a.scheduleNext(x, r) }}
	a.cloudResolve(response, request)
	return true
}

func (a *App) authorizeCloudPlayback(w http.ResponseWriter, r *http.Request, m cloudMount, p string) bool {
	if item, ok := r.Context().Value(cloudPlaybackItemKey{}).(string); ok && item != "" {
		user, err := a.auth(r)
		if err != nil || user.API {
			fail(w, 401, "播放保护模式需要用户登录")
			return false
		}
		x, err := a.itemForUser(r.WithContext(context.WithValue(r.Context(), mediaViewerKey{}, user)), item)
		u, mount, sourcePath, valid := cloudSourceURL(x.URL)
		if err != nil || !valid || mount != m.ID || sourcePath != p || !validCloudSourceSign(m, p, u.Query().Get("sign")) {
			fail(w, 403, "无权播放这个媒体源")
			return false
		}
		if err := a.reserveContext(r.Context(), user, x.ID); err != nil {
			playbackAdmissionError(w, r, err)
			return false
		}
		return true
	}
	if validCloudInternalRequest(r, m, p, time.Now()) {
		return true
	}
	fail(w, 401, "播放保护模式已开启，请通过登录后的播放入口访问")
	return false
}

func cloudInternalSignature(m cloudMount, p, expiry string) string {
	h := hmac.New(sha256.New, []byte(m.Secret))
	h.Write([]byte("cloud-internal-v1\x00" + m.ID + "\x00" + p + "\x00" + expiry))
	return hex.EncodeToString(h.Sum(nil))
}

func validCloudInternalRequest(r *http.Request, m cloudMount, p string, now time.Time) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return false
	}
	q := r.URL.Query()
	if len(q["internal_expires"]) != 1 || len(q["internal_token"]) != 1 {
		return false
	}
	expiry := q.Get("internal_expires")
	stamp, err := strconv.ParseInt(expiry, 10, 64)
	if err != nil || stamp <= now.Unix() || stamp > now.Add(cloudInternalLifetime).Unix() {
		return false
	}
	provided, err := hex.DecodeString(q.Get("internal_token"))
	expected, _ := hex.DecodeString(cloudInternalSignature(m, p, expiry))
	return err == nil && hmac.Equal(provided, expected)
}

// FFmpeg/FFprobe tasks use a scoped, expiring credential on the private worker
// port. The public gateway rejects these parameters even though its forwarding
// connection to the worker is also loopback.
func (a *App) cloudInternalInput(raw string) (string, error) {
	u, mount, p, ok := cloudSourceURL(raw)
	if !ok {
		return raw, nil
	}
	enabled, err := a.cloudPlaybackProtection(context.Background())
	if err != nil || !enabled {
		return raw, err
	}
	m, err := a.cloudMount(mount)
	if err != nil || !m.Enabled || !validCloudSourceSign(m, p, u.Query().Get("sign")) {
		return "", errors.New("网盘媒体源已停用或签名无效")
	}
	expiry := strconv.FormatInt(time.Now().Add(cloudInternalLifetime).Unix(), 10)
	internal, _ := url.Parse(cloudInternalEndpoint)
	internal.Path = u.Path
	internal.RawQuery = url.Values{
		"path": {p}, "sign": {cloudSign(m, p)},
		"internal_expires": {expiry}, "internal_token": {cloudInternalSignature(m, p, expiry)},
	}.Encode()
	return internal.String(), nil
}
