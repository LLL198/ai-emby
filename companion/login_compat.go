package main

import (
	"net/http"
 "mime"
 "strings"
	"time"
)

func (a *App) loginSession(r *http.Request, u User, d string) M {
	client, version, name := field(r, "Client"), field(r, "Version"), field(r, "Device")
	if client == "" {
		client = r.Header.Get("X-Emby-Client")
		if client == "" {
			client = q(r, "X-Emby-Client")
		}
	}
	if version == "" {
		version = r.Header.Get("X-Emby-Client-Version")
		if version == "" {
			version = q(r, "X-Emby-Client-Version")
		}
	}
	if name == "" {
		name = r.Header.Get("X-Emby-Device-Name")
		if name == "" {
			name = q(r, "X-Emby-Device-Name")
		}
	}
	return M{"Id": id(), "ServerId": a.serverID, "UserId": u.ID, "UserName": u.Name,
		"Client": client, "ApplicationVersion": version, "DeviceId": d, "DeviceName": name,
		"LastActivityDate": time.Now().UTC().Format(time.RFC3339Nano), "LastPlaybackCheckIn": time.Now().UTC().Format(time.RFC3339Nano),
		"IsActive": true, "SupportsRemoteControl": false, "SupportsMediaControl": false,
		"AdditionalUsers": []M{}, "PlayableMediaTypes": []string{"Video"}, "SupportedCommands": []string{},
		"PlayState":       M{"CanSeek": false, "IsPaused": false, "IsMuted": false, "RepeatMode": "RepeatNone", "PlaybackOrder": "Default"},
		"NowPlayingQueue": []M{}, "NowPlayingQueueFullItems": []M{}}
}

type loginCredentials struct {
 Username string
 Pw string
}

func loginBody(w http.ResponseWriter, r *http.Request, b *loginCredentials) bool {
 mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
 if !strings.EqualFold(mediaType, "application/x-www-form-urlencoded") {
  return body(w, r, b)
 }
 r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
 if err := r.ParseForm(); err != nil {
  fail(w, http.StatusBadRequest, "无效表单")
  return false
 }
 for key, values := range r.PostForm {
  if len(values) == 0 { continue }
  switch {
  case strings.EqualFold(key, "Username"):
   b.Username = values[0]
  case strings.EqualFold(key, "Pw"):
   b.Pw = values[0]
  }
 }
 return true
}
