package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"path"
	"strings"
	"time"
)

type trackingVideoSummary struct {
	Count, Folders int
	Formats        map[string]int
	ParsedAt       int64
}

type trackingVideoRecord struct {
	trackingVideoSummary
	Key string
}

func trackingVideoKey(resource trackingResource) string {
	return digest(resource.URL + "\x00" + resource.Password)
}

func trackingAttachVideoSummary(resource *trackingResource, raw string) {
	var record trackingVideoRecord
	if json.Unmarshal([]byte(raw), &record) == nil && record.ParsedAt > 0 && record.Key == trackingVideoKey(*resource) {
		resource.VideoInfo = &record.trackingVideoSummary
	}
}

func (a *App) trackingVideoInfo(rid string) *trackingVideoSummary {
	if rid == "" {
		return nil
	}
	var resource trackingResource
	var raw, parsed string
	if a.db.QueryRow("SELECT r.data,p.data FROM feature_tracking_resources r JOIN feature_tracking_resource_parses p ON p.resource=r.id WHERE r.id=?", rid).Scan(&raw, &parsed) != nil || json.Unmarshal([]byte(raw), &resource) != nil {
		return nil
	}
	trackingAttachVideoSummary(&resource, parsed)
	return resource.VideoInfo
}

func trackingCountVideos(ctx context.Context, provider trackingShareProvider) (trackingVideoSummary, error) {
	info := trackingVideoSummary{Formats: map[string]int{}}
	directories, files := map[string]bool{}, map[string]bool{}
	visited := 0
	var walk func(string, int) error
	walk = func(parent string, depth int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if depth > 32 || len(directories) >= 2000 {
			return errors.New("分享目录过大，未完成全部统计，请选择范围更小的分享")
		}
		if directories[parent] {
			return errors.New("分享存在重复或循环目录，无法确认完整数量")
		}
		directories[parent] = true
		entries, err := provider.List(ctx, parent, true)
		if err != nil {
			return err
		}
		for _, file := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			visited++
			if visited > 100000 {
				return errors.New("分享文件过多，未完成全部统计，请选择范围更小的分享")
			}
			if file.ID == "" || !trackingSafeName(file.Name) {
				return errors.New("分享返回了无效文件信息，无法确认完整数量")
			}
			if file.Dir {
				info.Folders++
				if err = walk(file.ID, depth+1); err != nil {
					return err
				}
				continue
			}
			extension := strings.ToLower(path.Ext(strings.TrimSpace(file.Name)))
			if !cloudVideos[extension] || files[file.ID] {
				continue
			}
			files[file.ID] = true
			info.Count++
			info.Formats[strings.TrimPrefix(extension, ".")]++
		}
		return nil
	}
	err := walk(provider.ShareRoot(), 0)
	if err == nil {
		info.ParsedAt = time.Now().Unix()
	}
	return info, err
}

func (a *App) trackingParseAPI(w http.ResponseWriter, r *http.Request) {
	if !featureMethod(w, r, "POST") {
		return
	}
	var b struct{ ResourceID, MountID string }
	if !body(w, r, &b) {
		return
	}
	if b.ResourceID == "" || len(b.ResourceID) > 128 || b.MountID == "" || len(b.MountID) > 128 {
		fail(w, 400, "请选择分享资源和同类网盘账号")
		return
	}
	var resource trackingResource
	var raw string
	err := a.db.QueryRow("SELECT data FROM feature_tracking_resources WHERE id=?", b.ResourceID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		fail(w, 404, "分享资源不存在，请刷新列表")
		return
	}
	if err == nil {
		err = json.Unmarshal([]byte(raw), &resource)
	}
	if err != nil {
		featureError(w, err)
		return
	}
	resource.ID = b.ResourceID
	mount, err := a.cloudMount(b.MountID)
	if err != nil || !mount.Enabled || trackingMountCloud(mount.Driver) == "" || trackingMountCloud(mount.Driver) != resource.Cloud {
		fail(w, 400, "请选择已启用的同类网盘账号解析分享")
		return
	}
	if !a.features.trackingParseMu.TryLock() {
		fail(w, 409, "正在解析其他分享，请等待解析完成")
		return
	}
	defer a.features.trackingParseMu.Unlock()
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()
	if trackingMobileIncomplete(resource) {
		var subscription trackingSubscription
		if err = a.db.QueryRow("SELECT data FROM feature_tracking_subscriptions WHERE id=?", resource.Subscription).Scan(&raw); err != nil || json.Unmarshal([]byte(raw), &subscription) != nil {
			fail(w, 400, "分享上下文无法读取，请重新搜索")
			return
		}
		resource, err = a.trackingRepairShare(ctx, subscription, resource)
		if err != nil {
			fail(w, 502, err.Error())
			return
		}
	}
	provider, err := trackingOpenShare(ctx, mount, resource)
	var info trackingVideoSummary
	if err == nil {
		info, err = trackingCountVideos(ctx, provider)
	}
	if err != nil {
		if ctx.Err() != nil {
			err = errors.New("解析已取消或超时，未完成全部统计，请重新解析")
		}
		fail(w, 502, err.Error())
		return
	}
	record := trackingVideoRecord{trackingVideoSummary: info, Key: trackingVideoKey(resource)}
	_, err = a.db.Exec("INSERT INTO feature_tracking_resource_parses(resource,data) VALUES(?,?) ON CONFLICT(resource) DO UPDATE SET data=excluded.data", b.ResourceID, featureJSON(record))
	if err != nil {
		featureError(w, err)
		return
	}
	respond(w, M{"ResourceID": b.ResourceID, "VideoInfo": info})
}
