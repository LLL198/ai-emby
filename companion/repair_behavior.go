package main

import (
	"context"
	"fmt"
	"net/http"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

func (a *App) mediaLabel(x Item) string {
	if x.Kind != "Episode" {
		return x.Name
	}
	name := x.Name
	if p, e := a.item(x.Parent); e == nil {
		if p.Kind == "Season" {
			if s, e := a.item(p.Parent); e == nil {
				name = s.Name
			}
		} else {
			name = p.Name
		}
	}
	return fmt.Sprintf("%s 第%d季 第%d集", name, x.Season, x.Episode)
}

// Delay work until after the player has received its redirect. Recheck the setting
// when the timer fires, so disabling it also cancels pending automatic starts.
func (a *App) scheduleNext(x Item, r *http.Request) {
	if x.Kind != "Episode" || r.Method == http.MethodHead || strings.EqualFold(q(r, "GoEmbyProbe"), "true") || !a.probeSettings().PreloadNext {
		return
	}
	a.probes.mu.Lock()
	if a.probes.next == nil {
		a.probes.next = map[string]time.Time{}
	}
	now := time.Now()
	for k, t := range a.probes.next {
		if now.Sub(t) > time.Minute {
			delete(a.probes.next, k)
		}
	}
	if _, ok := a.probes.next[x.ID]; ok {
		a.probes.mu.Unlock()
		return
	}
	a.probes.next[x.ID] = now
	a.probes.mu.Unlock()
	clone := r.Clone(context.Background())
	time.AfterFunc(3*time.Second, func() {
		if a.probeSettings().PreloadNext {
			a.preloadNext(x, clone)
		}
	})
}

var probeURL = regexp.MustCompile(`https?://[^\s"']+`)

func probeFailure(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return fmt.Errorf("媒体信息提取超时（60秒），未写入完整信息")
	}
	if e, ok := err.(*exec.ExitError); ok {
		msg := probeURL.ReplaceAllString(string(e.Stderr), "[媒体地址已隐藏]")
		msg = strings.TrimSpace(msg)
		if len(msg) > 1500 {
			msg = msg[:1500]
		}
		return fmt.Errorf("ffprobe 提取失败（退出码 %d）：%s", e.ExitCode(), msg)
	}
	return fmt.Errorf("无法启动 ffprobe：%w", err)
}
func (a *App) loginSuccess(r *http.Request, u User, d string) {
	key := a.newActivity("playback", "", "用户登录成功")
	a.changeActivity(key, func(v *activityEntry) {
		v.State = "login"
		v.Username = u.Name
		v.UserID = u.ID
		v.DeviceKey = d
		v.Device = d
		v.Client = r.UserAgent()
		v.IP = r.RemoteAddr
	})
}

type clientTicks int64

func (t *clientTicks) UnmarshalJSON(b []byte) error {
	n, e := strconv.ParseInt(strings.Trim(string(b), "\""), 10, 64)
	if e == nil {
		*t = clientTicks(n)
	}
	return e
}
