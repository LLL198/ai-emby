package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type cloudTransferTarget struct {
	MountID, Path string
}

type cloudTransferConfig struct {
	ResourceID, Title, SourceMountID, SourcePath, Format string
	Targets                                              []cloudTransferTarget
	Limit, Concurrency, CacheLimitGB                     int
	KeepLocal                                            bool
}

type cloudTransferFile struct {
	ID, Name, Relative, ShareParent, Remote, Output string
	ShareID                                         string
	State, Stage, Error, PendingTask, PendingParent string
	SourceSize, OutputSize, Bytes                   int64
	SourceStamp, DownloadETag, OutputSHA256         string
	Transferred, Downloaded, Remuxed                bool
	Uploads                                         map[string]string
}

type cloudTransferTask struct {
	ID, State, Stage, Error, Folder string
	Config                          cloudTransferConfig
	Files                           []cloudTransferFile
	PlanReady, Limited              bool
	Created, Updated                int64
	resource                        trackingResource
}

type cloudTransferRun struct {
	app          *App
	task         cloudTransferTask
	mu           sync.Mutex
	reservations map[string]int64
	lastSave     time.Time
	ctx          context.Context
}

const cloudTransferSchema = `CREATE TABLE IF NOT EXISTS feature_cloud_transfers (
 id TEXT PRIMARY KEY, state TEXT NOT NULL, control TEXT NOT NULL DEFAULT '',
 data TEXT NOT NULL, resource TEXT NOT NULL, created BIGINT NOT NULL, updated BIGINT NOT NULL);
 CREATE INDEX IF NOT EXISTS feature_cloud_transfers_queue ON feature_cloud_transfers(state,created,id);`

func cloudTransferCache(id string) string {
	return filepath.Join(featureDataRoot(), "cloud-transfers", id)
}

func (a *App) cloudTransferRead(id string) (cloudTransferTask, error) {
	var task cloudTransferTask
	var raw, resource string
	err := a.db.QueryRow("SELECT data,resource,state FROM feature_cloud_transfers WHERE id=?", id).Scan(&raw, &resource, &task.State)
	if err != nil {
		return task, err
	}
	state := task.State
	if err = json.Unmarshal([]byte(raw), &task); err != nil {
		return task, err
	}
	task.State = state
	err = json.Unmarshal([]byte(resource), &task.resource)
	return task, err
}

func (run *cloudTransferRun) saveLocked() error {
	run.task.Updated = time.Now().Unix()
	_, err := run.app.db.Exec("UPDATE feature_cloud_transfers SET data=?,state=?,updated=? WHERE id=?", featureJSON(run.task), run.task.State, run.task.Updated, run.task.ID)
	run.lastSave = time.Now()
	return err
}

func (run *cloudTransferRun) change(index int, update func(*cloudTransferFile)) error {
	run.mu.Lock()
	defer run.mu.Unlock()
	update(&run.task.Files[index])
	return run.saveLocked()
}

func (run *cloudTransferRun) progress(index int, bytes int64) {
	run.mu.Lock()
	defer run.mu.Unlock()
	run.task.Files[index].Bytes = bytes
	if time.Since(run.lastSave) >= time.Second {
		_ = run.saveLocked()
	}
}

func cloudTransferPublic(task cloudTransferTask, detail bool) M {
	done, failed := 0, 0
	files := []cloudTransferFile{}
	for _, file := range task.Files {
		if file.State == "complete" {
			done++
		}
		if file.State == "error" {
			failed++
		}
		file.PendingTask, file.PendingParent, file.ShareParent, file.DownloadETag = "", "", "", ""
		file.ShareID = ""
		if detail {
			files = append(files, file)
		}
	}
	return M{"ID": task.ID, "State": task.State, "Stage": task.Stage, "Error": task.Error,
		"Config": task.Config, "Folder": task.Folder, "Total": len(task.Files), "Done": done, "Failed": failed,
		"Limited": task.Limited, "Created": task.Created, "Updated": task.Updated,
		"Files": files, "CachePath": cloudTransferCache(task.ID)}
}

func (a *App) cloudTransferAPI(w http.ResponseWriter, r *http.Request) {
	switch strings.TrimPrefix(r.URL.Path, "/admin/features/cloud-transfer") {
	case "", "/options":
		if !featureMethod(w, r, http.MethodGet) {
			return
		}
		rows, err := a.db.Query("SELECT id FROM feature_cloud_mounts ORDER BY created,id")
		if err != nil {
			featureError(w, err)
			return
		}
		ids := []string{}
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				break
			}
			ids = append(ids, id)
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			featureError(w, err)
			return
		}
		mounts := []M{}
		for _, id := range ids {
			if mount, err := a.cloudMount(id); err == nil {
				mounts = append(mounts, M{"ID": id, "Name": mount.Name, "Driver": mount.Driver,
					"Cloud": trackingMountCloud(mount.Driver), "Enabled": mount.Enabled,
					"TransferSupported": trackingMountCloud(mount.Driver) != ""})
			}
		}
		_, ffmpegErr := exec.LookPath("ffmpeg")
		_, probeErr := exec.LookPath("ffprobe")
		hint := a.trackingImportHint(r.URL.Query().Get("ResourceID"), "")
		respond(w, M{"Mounts": mounts, "Tasks": a.cloudTransferList(), "SuggestedTitle": hint.Title, "CacheRoot": filepath.Dir(cloudTransferCache("task")), "RemuxReady": ffmpegErr == nil && probeErr == nil})
	case "/task":
		if !featureMethod(w, r, http.MethodGet) {
			return
		}
		task, err := a.cloudTransferRead(r.URL.Query().Get("ID"))
		if err != nil {
			featureError(w, err)
			return
		}
		respond(w, cloudTransferPublic(task, true))
	case "/start":
		a.cloudTransferStart(w, r)
	case "/action":
		a.cloudTransferAction(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (a *App) cloudTransferList() []M {
	rows, err := a.db.Query("SELECT data,state FROM feature_cloud_transfers ORDER BY created DESC,id DESC LIMIT 100")
	if err != nil {
		return []M{}
	}
	defer rows.Close()
	out := []M{}
	for rows.Next() {
		var raw, state string
		var task cloudTransferTask
		if rows.Scan(&raw, &state) == nil && json.Unmarshal([]byte(raw), &task) == nil {
			task.State = state
			out = append(out, cloudTransferPublic(task, false))
		}
	}
	return out
}

func (a *App) cloudTransferStart(w http.ResponseWriter, r *http.Request) {
	if !featureMethod(w, r, http.MethodPost) {
		return
	}
	var config cloudTransferConfig
	if !body(w, r, &config) {
		return
	}
	var resource trackingResource
	var raw, cloud string
	if a.db.QueryRow("SELECT data,cloud FROM feature_tracking_resources WHERE id=? AND status<>'ignored'", config.ResourceID).Scan(&raw, &cloud) != nil || json.Unmarshal([]byte(raw), &resource) != nil {
		fail(w, 404, "搜索资源不存在，请重新搜索并选择资源")
		return
	}
	resource.ID, resource.Cloud = config.ResourceID, cloud
	if err := a.cloudTransferValidate(&config, resource); err != nil {
		fail(w, 400, err.Error())
		return
	}
	task := cloudTransferTask{ID: id(), State: "queued", Stage: "plan", Config: config, Files: []cloudTransferFile{}, Created: time.Now().Unix(), Updated: time.Now().Unix()}
	task.Folder = config.Title + " [" + task.ID[:8] + "]"
	_, err := a.db.Exec("INSERT INTO feature_cloud_transfers(id,state,data,resource,created,updated) VALUES(?,?,?,?,?,?)", task.ID, task.State, featureJSON(task), featureJSON(resource), task.Created, task.Updated)
	if err != nil {
		featureError(w, err)
		return
	}
	respond(w, cloudTransferPublic(task, false))
}

func (a *App) cloudTransferValidate(config *cloudTransferConfig, resource trackingResource) error {
	for _, command := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(command); err != nil {
			return errors.New("服务器需要安装 FFmpeg 和 FFprobe 后才能无损处理")
		}
	}
	mount, err := a.cloudMount(config.SourceMountID)
	if err != nil || !mount.Enabled || trackingMountCloud(mount.Driver) != resource.Cloud || trackingMountCloud(mount.Driver) == "" {
		return errors.New("请选择与分享同平台且已启用的转存网盘账号")
	}
	if _, _, err = trackingShareCode(resource, mount.Driver); err != nil {
		return err
	}
	config.Title = strings.TrimSpace(config.Title)
	if config.Title == "" {
		config.Title = resource.Title
	}
	config.Title, err = cloudLocalName(config.Title)
	if err != nil || len(config.Title) > 180 {
		return errors.New("请填写不超过 180 字节且不含路径分隔符的任务名称")
	}
	config.SourcePath, err = cloudPath(config.SourcePath)
	if err != nil {
		return err
	}
	if len(config.Targets) == 0 || len(config.Targets) > 8 {
		return errors.New("请选择 1–8 个上传目标网盘")
	}
	seen := map[string]bool{}
	for index := range config.Targets {
		target := &config.Targets[index]
		mount, err := a.cloudMount(target.MountID)
		if err != nil || !mount.Enabled || seen[target.MountID] {
			return errors.New("上传目标应为不同的已启用网盘账号")
		}
		seen[target.MountID] = true
		if target.Path, err = cloudPath(target.Path); err != nil {
			return err
		}
		if target.MountID == config.SourceMountID && target.Path == config.SourcePath {
			return errors.New("上传目录应与原始文件的转存目录不同，以保留原文件")
		}
	}
	if config.Format == "" {
		config.Format = "mkv"
	}
	if config.Format != "mkv" && config.Format != "mp4" {
		return errors.New("无损封装格式请选择 MKV 或 MP4")
	}
	if config.Limit == 0 {
		config.Limit = 200
	}
	if config.Concurrency == 0 {
		config.Concurrency = 1
	}
	if config.CacheLimitGB == 0 {
		config.CacheLimitGB = 100
	}
	if config.Limit < 1 || config.Limit > 5000 || config.Concurrency < 1 || config.Concurrency > 4 || config.CacheLimitGB < 1 || config.CacheLimitGB > 100000 {
		return errors.New("视频数范围 1–5000，并发范围 1–4，任务临时空间范围 1–100000 GiB")
	}
	return nil
}

func (a *App) cloudTransferAction(w http.ResponseWriter, r *http.Request) {
	if !featureMethod(w, r, http.MethodPost) {
		return
	}
	var request struct {
		ID, Action                string
		Concurrency, CacheLimitGB int
	}
	if !body(w, r, &request) {
		return
	}
	a.features.mu.Lock()
	defer a.features.mu.Unlock()
	task, err := a.cloudTransferRead(request.ID)
	if err != nil {
		featureError(w, err)
		return
	}
	if request.Action == "resume" {
		if task.State != "paused" && task.State != "error" && task.State != "cancelled" {
			fail(w, 409, "此任务无需恢复")
			return
		}
		if request.Concurrency != 0 {
			if request.Concurrency < 1 || request.Concurrency > 4 {
				fail(w, 400, "并发范围 1–4")
				return
			}
			task.Config.Concurrency = request.Concurrency
		}
		if request.CacheLimitGB != 0 {
			if request.CacheLimitGB < 1 || request.CacheLimitGB > 100000 {
				fail(w, 400, "临时空间范围 1–100000 GiB")
				return
			}
			task.Config.CacheLimitGB = request.CacheLimitGB
		}
		task.State, task.Error = "queued", ""
		_, err = a.db.Exec("UPDATE feature_cloud_transfers SET state='queued',control='',data=?,updated=? WHERE id=? AND state IN ('paused','error','cancelled')", featureJSON(task), time.Now().Unix(), task.ID)
	} else if request.Action == "pause" || request.Action == "cancel" {
		if task.State == "running" {
			_, err = a.db.Exec("UPDATE feature_cloud_transfers SET control=? WHERE id=? AND state='running'", request.Action, task.ID)
			if cancel := a.features.jobs["cloud-transfer:"+task.ID]; err == nil && cancel != nil {
				cancel()
			}
		} else if task.State == "queued" || task.State == "paused" || task.State == "error" {
			state := "paused"
			if request.Action == "cancel" {
				state = "cancelled"
			}
			_, err = a.db.Exec("UPDATE feature_cloud_transfers SET state=?,control='',updated=? WHERE id=? AND state<>'running'", state, time.Now().Unix(), task.ID)
		} else {
			fail(w, 409, "此任务已结束")
			return
		}
	} else {
		fail(w, 400, "请选择暂停、取消或恢复")
		return
	}
	if err != nil {
		featureError(w, err)
		return
	}
	respond(w, M{"ok": true})
}

func (a *App) cloudTransferBackground(ctx context.Context) {
	for ctx.Err() == nil {
		var taskID string
		a.features.mu.Lock()
		err := a.db.QueryRow("UPDATE feature_cloud_transfers SET state='running',control='' WHERE id=(SELECT id FROM feature_cloud_transfers WHERE state='queued' ORDER BY created,id LIMIT 1 FOR UPDATE SKIP LOCKED) RETURNING id").Scan(&taskID)
		var cancel context.CancelFunc
		var taskCtx context.Context
		if err == nil {
			taskCtx, cancel = context.WithCancel(ctx)
			a.features.jobs["cloud-transfer:"+taskID] = cancel
		}
		a.features.mu.Unlock()
		if err == nil {
			a.runCloudTransfer(taskCtx, taskID)
			cancel()
			a.features.mu.Lock()
			delete(a.features.jobs, "cloud-transfer:"+taskID)
			a.features.mu.Unlock()
		}
		if trackingPause(ctx, 2*time.Second) != nil {
			return
		}
	}
}

func (a *App) runCloudTransfer(ctx context.Context, id string) {
	task, err := a.cloudTransferRead(id)
	if err != nil {
		_, _ = a.db.Exec("UPDATE feature_cloud_transfers SET state='error' WHERE id=?", id)
		return
	}
	run := cloudTransferRun{app: a, task: task, ctx: ctx}
	activity := a.newActivity("cloud-transfer", id, "资源搬运 · "+task.Config.Title)
	a.changeActivity(activity, func(entry *activityEntry) {
		entry.State = "running"
		entry.Current = "准备转存、下载、无损重封装和上传"
	})
	err = run.execute(activity)
	run.mu.Lock()
	defer run.mu.Unlock()
	run.task.State, run.task.Error = "complete", ""
	if err != nil {
		run.task.State, run.task.Error = "error", err.Error()
	}
	if ctx.Err() != nil {
		var control string
		_ = a.db.QueryRow("SELECT control FROM feature_cloud_transfers WHERE id=?", id).Scan(&control)
		run.task.State, run.task.Error = "paused", "任务已暂停，可从已保存进度恢复"
		if control == "cancel" {
			run.task.State, run.task.Error = "cancelled", "任务已取消，网盘文件和未完成文件的临时副本保留"
		}
	}
	if saveErr := run.saveLocked(); saveErr != nil {
		err = fmt.Errorf("保存任务进度失败：%w", saveErr)
	}
	a.finishActivity(activity, err)
}

func transferRemote(task cloudTransferTask, file cloudTransferFile, parent string) string {
	return path.Join(parent, task.Folder, file.Relative)
}
