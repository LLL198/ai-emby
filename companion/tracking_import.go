package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

type trackingImportConfig struct {
	Reselect                                                    bool `json:",omitempty"`
	Enabled                                                     bool
	MountID, RemotePath, Output, Library, PublicURL, ResourceID string
	Limit                                                       int
}
type trackingImportState struct {
	Stage, Error, ResourceID, ConfigKey, Remote, Output string
	PendingTask, PendingParent                          string
	PendingFiles                                        []trackingShareFile
	Updated                                             int64
	Saved                                               int
	ManualConfig                                        *trackingImportConfig `json:",omitempty"`
}
type trackingShareFile struct {
	ID, Name, Token, Parent string
	Size                    int64
	Dir                     bool
}
type trackingShareProvider interface {
	Root() string
	ShareRoot() string
	List(context.Context, string, bool) ([]trackingShareFile, error)
	Mkdir(context.Context, string, string) (string, error)
	Save(context.Context, string, []trackingShareFile) (string, error)
	Wait(context.Context, string) error
}

var errTrackingTransferFailed = errors.New("网盘转存任务未成功，请检查分享权限和网盘空间")
var errTrackingNoResource = errors.New("等待名称和年份匹配的分享")

func trackingMountCloud(driver string) string {
	switch driver {
	case "Quark":
		return "quark"
	case "115 Cloud":
		return "115"
	case "GuangYaPan":
		return "guangya"
	case "139Yun":
		return "mobile"
	}
	return ""
}
func (a *App) trackingImportState(sid string) (trackingImportState, error) {
	var state trackingImportState
	var raw string
	err := a.db.QueryRow("SELECT data FROM feature_tracking_imports WHERE subscription=?", sid).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	err = json.Unmarshal([]byte(raw), &state)
	return state, err
}
func (a *App) trackingSaveImport(sid string, state *trackingImportState) error {
	state.Updated = time.Now().Unix()
	_, err := a.db.Exec("INSERT INTO feature_tracking_imports(subscription,data) VALUES(?,?) ON CONFLICT(subscription) DO UPDATE SET data=excluded.data", sid, featureJSON(state))
	return err
}
func trackingImportKey(s trackingSubscription) string {
	c := s.AutoImport
	return digest(featureJSON([]string{s.Title, strconv.Itoa(s.Year), c.MountID, c.RemotePath, c.Output, c.Library, c.PublicURL, c.ResourceID}))
}
func trackingWorkName(s trackingSubscription) (string, error) {
	name := strings.TrimSpace(s.Title)
	if name == "" || strings.ContainsAny(name, "\\/\x00\r\n") || name == "." || name == ".." {
		return "", errors.New("自动入库作品名称不能含路径分隔符")
	}
	if s.Year > 0 {
		name += fmt.Sprintf(" (%d)", s.Year)
	}
	return cloudLocalName(name)
}
func (a *App) trackingValidateImport(s *trackingSubscription) error {
	c := &s.AutoImport
	if !c.Enabled {
		if s.ID != "" {
			old, err := a.trackingImportState(s.ID)
			if err != nil {
				return err
			}
			if old.PendingTask != "" && (old.ConfigKey != trackingImportKey(*s) || c.Reselect) {
				return errors.New("转存任务尚未结束，可以暂停自动入库，但请保留目标和分享配置")
			}
		}
		return nil
	}
	m, err := a.cloudMount(c.MountID)
	if err != nil || !m.Enabled {
		return errors.New("请选择已启用的网盘挂载")
	}
	if trackingMountCloud(m.Driver) == "" {
		return errors.New("此挂载尚不支持分享转存，请选择夸克、115 Cookie、移动云盘或光鸭")
	}
	if c.Limit == 0 {
		c.Limit = 200
	}
	if c.Limit < 1 || c.Limit > 5000 {
		return errors.New("每次转存视频数为 1–5000")
	}
	c.RemotePath, err = cloudPath(c.RemotePath)
	if err != nil {
		return err
	}
	relative, err := cloudOutput(c.Output)
	if err != nil {
		return err
	}
	c.Output = filepath.Join(fileRoot(), relative)
	name, err := trackingWorkName(*s)
	if err != nil {
		return err
	}
	output := filepath.Join(c.Output, name)
	found := false
	for _, lib := range a.libraries() {
		if lib["Id"] != c.Library {
			continue
		}
		for _, root := range lib["Locations"].([]string) {
			rel, e := filepath.Rel(root, output)
			if e == nil && (rel == "." || filepath.IsLocal(rel)) {
				found = true
			}
		}
	}
	if !found {
		return errors.New("STRM 输出目录需要位于所选媒体库内")
	}
	u, err := url.Parse(strings.TrimSpace(c.PublicURL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("请填写播放器可访问的 AI Emby 服务地址")
	}
	c.PublicURL = strings.TrimRight(u.String(), "/")
	if len(s.CloudTypes) > 0 && !trackingContains(s.CloudTypes, trackingMountCloud(m.Driver)) {
		return errors.New("搜索网盘类型必须包含自动入库的目标网盘")
	}
	if !a.scraperSettings().Enabled {
		return errors.New("请先在刮削模块开启刮削总开关")
	}
	if c.ResourceID != "" {
		var cloud string
		if a.db.QueryRow("SELECT cloud FROM feature_tracking_resources WHERE id=? AND subscription=? AND status<>'ignored'", c.ResourceID, s.ID).Scan(&cloud) != nil || cloud != trackingMountCloud(m.Driver) {
			return errors.New("所选分享不属于此订阅或与目标网盘类型不一致")
		}
	}
	if s.ID != "" {
		old, e := a.trackingImportState(s.ID)
		if e != nil {
			return e
		}
		if old.PendingTask != "" && (old.ConfigKey != trackingImportKey(*s) || c.Reselect) {
			return errors.New("网盘转存任务尚未确认结束，请先重试完成后再更改目标或分享")
		}
	}
	return nil
}
func (a *App) trackingImportAPI(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/admin/features/tracking/import/options" {
		if !featureMethod(w, r, "GET") {
			return
		}
		rows, err := a.db.Query("SELECT id,name,driver,enabled FROM feature_cloud_mounts ORDER BY created,id")
		if err != nil {
			featureError(w, err)
			return
		}
		mounts := []M{}
		for rows.Next() {
			var sid, name, driver string
			var enabled int
			if err = rows.Scan(&sid, &name, &driver, &enabled); err != nil {
				break
			}
			mounts = append(mounts, M{"ID": sid, "Name": name, "Driver": driver, "Cloud": trackingMountCloud(driver), "Supported": trackingMountCloud(driver) != "", "Enabled": enabled == 1})
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			featureError(w, err)
			return
		}
		respond(w, M{"Mounts": mounts, "Libraries": a.libraries(), "FileRoot": fileRoot(), "ScraperEnabled": a.scraperSettings().Enabled})
		return
	}
	if r.URL.Path == "/admin/features/tracking/import/start" {
		a.trackingStartImport(w, r)
		return
	}
	if !featureMethod(w, r, "POST") {
		return
	}
	a.features.trackingMu.Lock()
	if a.features.trackingBusy {
		a.features.trackingMu.Unlock()
		fail(w, 409, "追新任务正在执行，完成后再选择资源")
		return
	}
	var b struct{ ID, ResourceID string }
	if !body(w, r, &b) {
		a.features.trackingMu.Unlock()
		return
	}
	var raw string
	if a.db.QueryRow("SELECT data FROM feature_tracking_subscriptions WHERE id=?", b.ID).Scan(&raw) != nil {
		a.features.trackingMu.Unlock()
		fail(w, 404, "订阅不存在")
		return
	}
	var s trackingSubscription
	if json.Unmarshal([]byte(raw), &s) != nil || !s.AutoImport.Enabled {
		a.features.trackingMu.Unlock()
		fail(w, 400, "先在订阅中启用自动入库并选择目标目录")
		return
	}
	s.AutoImport.ResourceID = b.ResourceID
	s.AutoImport.Reselect = b.ResourceID == ""
	if err := a.trackingValidateImport(&s); err != nil {
		a.features.trackingMu.Unlock()
		fail(w, 400, err.Error())
		return
	}
	reset := s.AutoImport.Reselect
	s.AutoImport.Reselect = false
	_, err := a.db.Exec("UPDATE feature_tracking_subscriptions SET data=?,next_search=? WHERE id=?", featureJSON(s), time.Now().Unix(), s.ID)
	if err == nil && reset {
		_, err = a.db.Exec("DELETE FROM feature_tracking_imports WHERE subscription=?", s.ID)
	}
	a.features.trackingMu.Unlock()
	if err != nil {
		featureError(w, err)
		return
	}
	if !a.trackingQueue([]string{s.ID}) {
		fail(w, 409, "追新任务正在执行，请稍后重试")
		return
	}
	respond(w, M{"ok": true})
}

var trackingYearPattern = regexp.MustCompile(`(?:19|20)\d{2}`)
var trackingSeasonPattern = regexp.MustCompile(`(?i)^(?:season\s*\d+|s\d{1,3}|第[一二三四五六七八九十百\d]+[季集]|specials?|正片|剧集|电影|视频|4k|1080p|2160p|720p)$`)

func trackingTitleKey(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}
func trackingTitleMatch(title string, s trackingSubscription) bool {
	key := trackingTitleKey(s.Title)
	if key == "" || !strings.Contains(trackingTitleKey(title), key) {
		return false
	}
	if s.Year > 0 {
		years := trackingYearPattern.FindAllString(title, -1)
		if len(years) > 0 && !trackingContains(years, strconv.Itoa(s.Year)) {
			return false
		}
	}
	return true
}
func (a *App) trackingImportResource(s trackingSubscription, st trackingImportState, cloud string) (trackingResource, error) {
	pin := s.AutoImport.ResourceID
	if pin == "" && st.ConfigKey == trackingImportKey(s) {
		pin = st.ResourceID
	}
	rows, err := a.db.Query("SELECT id,data FROM feature_tracking_resources WHERE subscription=? AND cloud=? AND status<>'ignored' ORDER BY updated DESC,id", s.ID, cloud)
	if err != nil {
		return trackingResource{}, err
	}
	defer rows.Close()
	var candidate trackingResource
	for rows.Next() {
		var rid, raw string
		if err = rows.Scan(&rid, &raw); err != nil {
			return candidate, err
		}
		var resource trackingResource
		if err = json.Unmarshal([]byte(raw), &resource); err != nil {
			return candidate, err
		}
		resource.ID = rid
		if pin != "" && rid == pin {
			return resource, nil
		}
		if pin == "" && candidate.ID == "" && trackingTitleMatch(resource.Title, s) {
			candidate = resource
		}
	}
	if err = rows.Err(); err != nil {
		return candidate, err
	}
	if pin != "" {
		return candidate, errors.New("固定的分享已忽略或不可用，请在资源列表重新选择")
	}
	if candidate.ID == "" {
		return candidate, errTrackingNoResource
	}
	return candidate, nil
}
func trackingPause(ctx context.Context, duration time.Duration) error {
	t := time.NewTimer(duration)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
func trackingSafeName(s string) bool {
	return s != "" && s != "." && s != ".." && !strings.ContainsAny(s, "\\/\x00\r\n")
}
func trackingEnsureDirectory(ctx context.Context, p trackingShareProvider, parent, directory string) (string, error) {
	for _, segment := range strings.Split(strings.Trim(directory, "/"), "/") {
		if segment == "" {
			continue
		}
		if !trackingSafeName(segment) {
			return "", errors.New("网盘目录名无效")
		}
		list, err := p.List(ctx, parent, false)
		if err != nil {
			return "", err
		}
		found := false
		for _, entry := range list {
			if entry.Name != segment {
				continue
			}
			if !entry.Dir {
				return "", errors.New("目标目录存在同名文件")
			}
			parent, found = entry.ID, true
			break
		}
		if !found {
			parent, err = p.Mkdir(ctx, parent, segment)
			if err != nil {
				return "", err
			}
		}
	}
	return parent, nil
}
func (a *App) trackingStartImport(w http.ResponseWriter, r *http.Request) {
	if !featureMethod(w, r, "POST") {
		return
	}
	var b struct {
		ID, ResourceID string
		Config         trackingImportConfig
	}
	if !body(w, r, &b) {
		return
	}
	a.features.trackingMu.Lock()
	defer a.features.trackingMu.Unlock()
	if a.features.trackingBusy || a.features.ctx.Err() != nil {
		fail(w, 409, "追新或入库任务正在执行，请稍后重试")
		return
	}
	var raw string
	if a.db.QueryRow("SELECT data FROM feature_tracking_subscriptions WHERE id=?", b.ID).Scan(&raw) != nil {
		fail(w, 404, "订阅不存在")
		return
	}
	var s trackingSubscription
	if json.Unmarshal([]byte(raw), &s) != nil {
		fail(w, 400, "订阅配置无法读取")
		return
	}
	if b.ResourceID == "" {
		fail(w, 400, "请选择要入库的分享资源")
		return
	}
	s.AutoImport = b.Config
	s.AutoImport.Enabled = true
	s.AutoImport.ResourceID = b.ResourceID
	s.AutoImport.Reselect = false
	s.CloudTypes = nil
	if err := a.trackingValidateImport(&s); err != nil {
		fail(w, 400, err.Error())
		return
	}
	st, err := a.trackingImportState(s.ID)
	if err != nil {
		featureError(w, err)
		return
	}
	key := trackingImportKey(s)
	if st.ConfigKey != key {
		st = trackingImportState{ConfigKey: key}
	}
	st.Stage, st.Error, st.ResourceID = "queued", "", b.ResourceID
	config := s.AutoImport
	st.ManualConfig = &config
	if err = a.trackingSaveImport(s.ID, &st); err != nil {
		featureError(w, err)
		return
	}
	a.features.trackingBusy = true
	select {
	case a.features.trackingQueue <- trackingJob{Manual: &s}:
		respond(w, M{"ok": true})
	default:
		a.features.trackingBusy = false
		st.Stage, st.Error = "interrupted", "入库队列繁忙，请重试"
		_ = a.trackingSaveImport(s.ID, &st)
		fail(w, 409, st.Error)
	}
}

func (a *App) trackingManualImport(parent context.Context, s trackingSubscription) {
	defer func() { a.features.trackingMu.Lock(); a.features.trackingBusy = false; a.features.trackingMu.Unlock() }()
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	task := a.newActivity("tracking", "", "资源入库 · "+s.Title)
	a.features.mu.Lock()
	a.features.jobs["tracking:"+task] = cancel
	a.features.mu.Unlock()
	defer func() { a.features.mu.Lock(); delete(a.features.jobs, "tracking:"+task); a.features.mu.Unlock() }()
	a.changeActivity(task, func(v *activityEntry) { v.State = "running"; v.Total = 1; v.Current = s.Title + " · 准备入库" })
	err := a.trackingImport(ctx, s, task, true)
	if err == nil {
		a.changeActivity(task, func(v *activityEntry) { v.Done = 1; v.Current = s.Title + " · 入库完成" })
	}
	a.finishActivity(task, err)
}

func (a *App) trackingAutoImport(ctx context.Context, s trackingSubscription, activity string) error {
	return a.trackingImport(ctx, s, activity, false)
}
func (a *App) trackingImport(ctx context.Context, s trackingSubscription, activity string, manual bool) (result error) {
	st, err := a.trackingImportState(s.ID)
	if err != nil {
		return err
	}
	previousStage := st.Stage
	defer func() {
		if result != nil {
			st.Error = result.Error()
			if ctx.Err() != nil {
				st.Stage = "interrupted"
			} else {
				st.Stage = "error"
			}
		}
		if e := a.trackingSaveImport(s.ID, &st); result == nil {
			result = e
		}
	}()
	if err = a.trackingValidateImport(&s); err != nil {
		return err
	}
	key := trackingImportKey(s)
	if st.ConfigKey != key {
		st = trackingImportState{ConfigKey: key}
		previousStage = ""
	}
	if manual {
		config := s.AutoImport
		st.ManualConfig = &config
	} else {
		st.ManualConfig = nil
	}
	m, err := a.cloudMount(s.AutoImport.MountID)
	if err != nil {
		return errors.New("目标网盘已移除")
	}
	resource, err := a.trackingImportResource(s, st, trackingMountCloud(m.Driver))
	if err != nil {
		st.Stage = "waiting-resource"
		if errors.Is(err, errTrackingNoResource) {
			st.Error = ""
			return nil
		}
		return err
	}
	name, err := trackingWorkName(s)
	if err != nil {
		return err
	}
	st.ResourceID, st.Remote, st.Output = resource.ID, path.Join(s.AutoImport.RemotePath, name), filepath.Join(s.AutoImport.Output, name)
	cloudManageMu.Lock()
	for _, output := range cloudOutputs {
		if pathsOverlap(output, st.Output) {
			cloudManageMu.Unlock()
			return errors.New("STRM 输出目录有其他生成任务，请稍后重试")
		}
	}
	if a.cloudJobRunning(m.ID) {
		cloudManageMu.Unlock()
		return errors.New("目标挂载有其他生成任务，请稍后重试")
	}
	workerCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	cloudOutputs[m.ID] = st.Output
	a.features.mu.Lock()
	a.features.jobs["cloud:"+m.ID] = cancel
	a.features.mu.Unlock()
	cloudManageMu.Unlock()
	defer func() {
		cloudManageMu.Lock()
		delete(cloudOutputs, m.ID)
		a.features.mu.Lock()
		delete(a.features.jobs, "cloud:"+m.ID)
		a.features.mu.Unlock()
		cloudManageMu.Unlock()
	}()
	ctx = workerCtx
	phase := func(stage, label string) error {
		st.Stage, st.Error = stage, ""
		a.changeActivity(activity, func(v *activityEntry) { v.Current = s.Title + " · " + label })
		return a.trackingSaveImport(s.ID, &st)
	}
	if err = phase("transfer", "转存分享"); err != nil {
		return err
	}
	p, err := trackingOpenShare(ctx, m, resource)
	if err != nil {
		return err
	}
	if st.PendingTask != "" {
		if err = p.Wait(ctx, st.PendingTask); err != nil {
			if errors.Is(err, errTrackingTransferFailed) {
				st.PendingTask, st.PendingParent, st.PendingFiles = "", "", nil
			}
			return err
		}
		if err = trackingConfirmSaved(ctx, p, st.PendingParent, st.PendingFiles); err != nil {
			return err
		}
		st.Saved += len(st.PendingFiles)
		st.PendingTask, st.PendingParent, st.PendingFiles = "", "", nil
		if err = a.trackingSaveImport(s.ID, &st); err != nil {
			return err
		}
	}
	destination, err := trackingEnsureDirectory(ctx, p, p.Root(), st.Remote)
	if err != nil {
		return err
	}
	added, found, visited := 0, 0, 0
	seen := map[string]bool{}
	var walk func(string, string, bool, int) error
	walk = func(source, target string, accepted bool, depth int) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if depth > 32 || visited > 10000 {
			return errors.New("分享目录过大，请选择范围更小的分享")
		}
		if seen[source] {
			return errors.New("分享存在重复或循环目录")
		}
		seen[source] = true
		entries, e := p.List(ctx, source, true)
		if e != nil {
			return e
		}
		own, e := p.List(ctx, target, false)
		if e != nil {
			return e
		}
		byName := map[string]trackingShareFile{}
		for _, f := range own {
			byName[f.Name] = f
		}
		matchedFolder := false
		if !accepted {
			for _, f := range entries {
				if f.Dir && trackingTitleMatch(f.Name, s) {
					matchedFolder = true
				}
			}
		}
		batch := []trackingShareFile{}
		flush := func() error {
			if len(batch) == 0 {
				return nil
			}
			task, e := p.Save(ctx, target, batch)
			if e != nil {
				return e
			}
			st.PendingTask, st.PendingParent, st.PendingFiles = task, target, append([]trackingShareFile(nil), batch...)
			if e = a.trackingSaveImport(s.ID, &st); e != nil {
				return e
			}
			if task != "" {
				if e = p.Wait(ctx, task); e != nil {
					if errors.Is(e, errTrackingTransferFailed) {
						st.PendingTask, st.PendingParent, st.PendingFiles = "", "", nil
					}
					return e
				}
			}
			if e = trackingConfirmSaved(ctx, p, target, batch); e != nil {
				return e
			}
			added += len(batch)
			st.Saved += len(batch)
			st.PendingTask, st.PendingParent, st.PendingFiles = "", "", nil
			batch = nil
			if e = a.trackingSaveImport(s.ID, &st); e != nil {
				return e
			}
			a.changeActivity(activity, func(v *activityEntry) { v.Current = fmt.Sprintf("%s · 已转存 %d 个新视频", s.Title, added) })
			return nil
		}
		for _, f := range entries {
			visited++
			if !trackingSafeName(f.Name) || f.ID == "" {
				return errors.New("分享文件名或 ID 无效")
			}
			if f.Dir {
				if e = flush(); e != nil {
					return e
				}
				match := trackingTitleMatch(f.Name, s)
				if !accepted && !match && (matchedFolder || !trackingSeasonPattern.MatchString(f.Name)) {
					continue
				}
				dest := target
				if depth != 0 || !match {
					dest, e = trackingEnsureDirectory(ctx, p, target, f.Name)
					if e != nil {
						return e
					}
				}
				if e = walk(f.ID, dest, accepted || match, depth+1); e != nil {
					return e
				}
				if dest == target {
					current, e := p.List(ctx, target, false)
					if e != nil {
						return e
					}
					for _, saved := range current {
						byName[saved.Name] = saved
					}
				}
				continue
			}
			if !cloudVideos[strings.ToLower(path.Ext(f.Name))] || (!accepted && !trackingTitleMatch(f.Name, s) && !trackingTitleMatch(resource.Title, s)) {
				continue
			}
			found++
			if existing, ok := byName[f.Name]; ok {
				if existing.Dir || (existing.Size > 0 && f.Size > 0 && existing.Size != f.Size) {
					return fmt.Errorf("目标存在不同内容的同名文件：%s，请手动处理", f.Name)
				}
				continue
			}
			if added+len(batch) >= s.AutoImport.Limit {
				continue
			}
			batch = append(batch, f)
			byName[f.Name] = f
			if len(batch) >= 50 {
				if e = flush(); e != nil {
					return e
				}
			}
		}
		return flush()
	}
	if err = walk(p.ShareRoot(), destination, false, 0); err != nil {
		return err
	}
	if found == 0 {
		return errors.New("分享中未找到此作品的视频，请选择正确的分享或检查命名")
	}
	if added == 0 && previousStage == "complete" {
		st.Stage, st.Error = "complete", ""
		return nil
	}
	if err = phase("strm", "生成 STRM"); err != nil {
		return err
	}
	ancestor := "/"
	if _, err = cloudList(ctx, m, ancestor, 1, true); err != nil {
		return err
	}
	segments := strings.Split(strings.Trim(st.Remote, "/"), "/")
	for _, segment := range segments[:len(segments)-1] {
		ancestor = path.Join(ancestor, segment)
		if _, err = cloudList(ctx, m, ancestor, 1, true); err != nil {
			return err
		}
	}
	if err = trackingRefreshCloud(ctx, m, st.Remote, 0); err != nil {
		return err
	}
	job := a.newActivity("cloud-strm", m.ID, "追新 · "+s.Title)
	err = a.cloudGenerate(ctx, m, cloudGenerateRequest{ID: m.ID, Source: st.Remote, Output: st.Output, PublicURL: s.AutoImport.PublicURL, Library: s.AutoImport.Library, Recursive: true, Concurrency: 4}, job)
	a.finishActivity(job, err)
	if err != nil {
		return err
	}
	if err = phase("scan", "扫描新视频"); err != nil {
		return err
	}
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if _, ok := a.reserveConcurrentScan(s.AutoImport.Library); ok {
			break
		}
		if err = trackingPause(ctx, 2*time.Second); err != nil {
			return err
		}
	}
	if err = a.runConcurrentScanResult(ctx, s.AutoImport.Library, true, false, []string{st.Output}); err != nil {
		return errors.New("媒体库扫描失败，请查看扫描日志")
	}
	if err = phase("scrape", "刮削新视频"); err != nil {
		return err
	}
	if err = a.trackingScrape(ctx, s.AutoImport.Library, st.Output); err != nil {
		return err
	}
	_, err = a.db.Exec("UPDATE feature_tracking_resources SET status='seen' WHERE id=? AND status<>'ignored'", resource.ID)
	if err != nil {
		return err
	}
	return phase("complete", fmt.Sprintf("入库完成 · 新增 %d 个视频", added))
}
func trackingConfirmSaved(ctx context.Context, p trackingShareProvider, parent string, files []trackingShareFile) error {
	for attempt := 0; attempt < 10; attempt++ {
		list, err := p.List(ctx, parent, false)
		if err != nil {
			return err
		}
		all := true
		for _, f := range files {
			found := false
			for _, saved := range list {
				if !saved.Dir && saved.Name == f.Name && (saved.Size == 0 || f.Size == 0 || saved.Size == f.Size) {
					found = true
					break
				}
			}
			all = all && found
		}
		if all {
			return nil
		}
		if err = trackingPause(ctx, time.Second); err != nil {
			return err
		}
	}
	return errors.New("转存结果尚未出现在目标目录，下次执行会重新核对")
}
func trackingRefreshCloud(ctx context.Context, m cloudMount, directory string, depth int) error {
	if depth > 32 {
		return errors.New("目标目录层级过深")
	}
	for page := 1; page <= 1000; page++ {
		list, err := cloudList(ctx, m, directory, page, true)
		if err != nil {
			return err
		}
		for _, f := range list.Content {
			if f.IsDir {
				if err = trackingRefreshCloud(ctx, m, path.Join(directory, f.Name), depth+1); err != nil {
					return err
				}
			}
		}
		if len(list.Content) < 200 {
			return nil
		}
	}
	return errors.New("目标目录文件过多")
}
func (a *App) trackingScrape(ctx context.Context, library, output string) error {
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		a.scraper.mu.Lock()
		config := a.scraperSettings()
		if !config.Enabled {
			a.scraper.mu.Unlock()
			return errors.New("刮削总开关已关闭，STRM 已生成，可开启后重试")
		}
		if a.scraper.running || a.scraper.planning || a.scraper.plan != nil {
			a.scraper.mu.Unlock()
			if err := trackingPause(ctx, 2*time.Second); err != nil {
				return err
			}
			continue
		}
		workerCtx, cancel := context.WithCancel(ctx)
		a.scraper.running, a.scraper.automatic, a.scraper.cancel = true, true, cancel
		a.scraper.mu.Unlock()
		defer cancel()
		config.ManualScopes, config.Overwrite = nil, false
		config.fileScope, _ = a.scraperItemScope(library, output)
		if config.fileScope == nil {
			a.scraper.mu.Lock()
			a.scraper.running, a.scraper.automatic, a.scraper.cancel = false, false, nil
			a.scraper.mu.Unlock()
			return errors.New("无法确定追新刮削目录")
		}
		activity := a.newActivity("scraper", "", "追新刮削计划")
		rows, err := a.db.QueryContext(workerCtx, "SELECT "+cols+" FROM items WHERE lib=$1 AND (path=$2 OR substr(path,1,length($3::text))=$3) AND kind IN ('Movie','Series','Season','Episode') ORDER BY path,kind", library, output, output+string(filepath.Separator))
		var items []Item
		if err == nil {
			for rows.Next() {
				var item Item
				item, err = readItem(rows)
				if err != nil {
					break
				}
				items = append(items, item)
			}
			if err == nil {
				err = rows.Err()
			}
			rows.Close()
		}
		var plan *scraperPlan
		if err == nil && len(items) == 0 {
			err = errors.New("新视频尚未进入媒体索引，请检查媒体库类型和文件命名")
		}
		if err == nil {
			plan, err = a.buildScraperItems(workerCtx, config, items, activity)
		}
		a.finishActivity(activity, err)
		if err != nil {
			a.scraper.mu.Lock()
			a.scraper.running, a.scraper.automatic, a.scraper.cancel = false, false, nil
			a.scraper.mu.Unlock()
			return a.scraperSafeError(err)
		}
		return a.runScraper(workerCtx, plan)
	}
}
