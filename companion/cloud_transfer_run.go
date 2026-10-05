package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

func (run *cloudTransferRun) execute(activity string) error {
	defer run.cleanupDownloadMount()
	config := run.task.Config
	needSource := !run.task.PlanReady
	for _, file := range run.task.Files {
		needSource = needSource || (file.State != "complete" && !run.allUploaded(file) && (!file.Transferred || !file.Remuxed))
	}
	var mount cloudMount
	var err error
	if needSource {
		mount, err = run.app.cloudMount(config.SourceMountID)
		if err != nil || !mount.Enabled {
			return errors.New("转存网盘已移除或暂停，请恢复挂载后重试")
		}
	}
	needShare := !run.task.PlanReady
	for _, file := range run.task.Files {
		needShare = needShare || (file.State != "complete" && !run.allUploaded(file) && !file.Transferred)
	}
	var provider trackingShareProvider
	if needShare {
		provider, err = trackingOpenShare(run.ctx, mount, run.task.resource)
		if err != nil {
			return err
		}
	}
	if !run.task.PlanReady {
		if err = run.plan(provider); err != nil {
			return err
		}
	}
	if err = run.prepareCache(); err != nil {
		return err
	}
	for index := range run.task.Files {
		if err = run.ctx.Err(); err != nil {
			return err
		}
		if run.task.Files[index].Transferred || run.task.Files[index].State == "complete" || run.allUploaded(run.task.Files[index]) {
			continue
		}
		if err = run.transfer(index, provider); err != nil {
			if run.ctx.Err() != nil {
				return run.ctx.Err()
			}
			if saveErr := run.change(index, func(file *cloudTransferFile) { file.State, file.Error = "error", err.Error() }); saveErr != nil {
				return saveErr
			}
		}
	}
	run.mu.Lock()
	run.task.Stage = "process"
	err = run.saveLocked()
	run.mu.Unlock()
	if err != nil {
		return err
	}
	var downloadMount cloudMount
	if needSource {
		// This private mount reads original bytes with the provider's required headers.
		downloadMount, err = run.prepareDownloadMount(mount)
		if err != nil {
			return err
		}
	}
	indices := []int{}
	for index, file := range run.task.Files {
		if file.State != "complete" && (file.Transferred || run.allUploaded(file)) {
			indices = append(indices, index)
		}
	}
	jobs := make(chan int)
	var workers sync.WaitGroup
	for worker := 0; worker < config.Concurrency; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for index := range jobs {
				if run.ctx.Err() != nil {
					continue
				}
				if e := run.process(index, downloadMount); e != nil && run.ctx.Err() == nil {
					_ = run.change(index, func(file *cloudTransferFile) { file.State, file.Error = "error", e.Error() })
				}
				run.mu.Lock()
				done := 0
				for _, file := range run.task.Files {
					if file.State == "complete" {
						done++
					}
				}
				current := run.task.Files[index].Name
				run.mu.Unlock()
				run.app.changeActivity(activity, func(entry *activityEntry) {
					entry.Total, entry.Done = len(run.task.Files), done
					entry.Current = current + " · 转存、下载、无损重封装、上传"
				})
			}
		}()
	}
dispatch:
	for _, index := range indices {
		select {
		case jobs <- index:
		case <-run.ctx.Done():
			break dispatch
		}
	}
	close(jobs)
	workers.Wait()
	if err = run.ctx.Err(); err != nil {
		return err
	}
	failed := 0
	for _, file := range run.task.Files {
		if file.State != "complete" {
			failed++
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d 个视频未完成，查看文件详情后重试；已完成上传的文件会跳过", failed)
	}
	return nil
}

func (run *cloudTransferRun) plan(provider trackingShareProvider) error {
	files := []cloudTransferFile{}
	seen, outputs := map[string]bool{}, map[string]bool{}
	visited, limited := 0, false
	var walk func(string, string, int) error
	walk = func(parent, relative string, depth int) error {
		if err := run.ctx.Err(); err != nil {
			return err
		}
		if depth > 32 || visited > 100000 || len(seen) > 2000 || seen[parent] {
			return errors.New("分享目录过大或存在循环，请选择范围更小的资源")
		}
		seen[parent] = true
		entries, err := provider.List(run.ctx, parent, true)
		if err != nil {
			return err
		}
		for _, file := range entries {
			visited++
			if visited > 100000 {
				return errors.New("分享目录文件过多，请选择范围更小的资源")
			}
			if !trackingSafeName(file.Name) || file.ID == "" {
				return errors.New("分享返回了无效文件名或文件 ID")
			}
			rel := path.Join(relative, file.Name)
			if file.Dir {
				if err = walk(file.ID, rel, depth+1); err != nil {
					return err
				}
				if limited {
					return nil
				}
				continue
			}
			if !cloudVideos[strings.ToLower(path.Ext(file.Name))] {
				continue
			}
			if len(files) >= run.task.Config.Limit {
				limited = true
				return nil
			}
			fileID := digest(run.task.ID + "\x00" + file.ID + "\x00" + rel)[:32]
			output := strings.TrimSuffix(rel, path.Ext(rel)) + "." + run.task.Config.Format
			if outputs[strings.ToLower(output)] {
				output = strings.TrimSuffix(output, path.Ext(output)) + "-" + fileID[:8] + path.Ext(output)
			}
			outputs[strings.ToLower(output)] = true
			files = append(files, cloudTransferFile{ID: fileID, ShareID: file.ID, Name: file.Name, Relative: rel, ShareParent: parent,
				SourceSize: file.Size, Output: output, State: "queued", Stage: "transfer", Uploads: map[string]string{}})
		}
		return nil
	}
	if err := walk(provider.ShareRoot(), "", 0); err != nil {
		return err
	}
	if len(files) == 0 {
		return errors.New("分享中没有可下载的视频文件")
	}
	run.mu.Lock()
	defer run.mu.Unlock()
	run.task.Files, run.task.PlanReady, run.task.Limited, run.task.Stage = files, true, limited, "transfer"
	return run.saveLocked()
}

func (run *cloudTransferRun) transfer(index int, provider trackingShareProvider) error {
	file := run.task.Files[index]
	if err := run.change(index, func(file *cloudTransferFile) { file.State, file.Stage, file.Error = "running", "transfer", "" }); err != nil {
		return err
	}
	remote := transferRemote(run.task, file, run.task.Config.SourcePath)
	parent, err := trackingEnsureDirectory(run.ctx, provider, provider.Root(), path.Dir(remote))
	if err != nil {
		return err
	}
	confirm := func() error {
		return trackingConfirmSaved(run.ctx, provider, parent, []trackingShareFile{{Name: file.Name, Size: file.SourceSize}})
	}
	if file.PendingTask != "" {
		if err = provider.Wait(run.ctx, file.PendingTask); err != nil {
			if errors.Is(err, errTrackingTransferFailed) {
				_ = run.change(index, func(file *cloudTransferFile) { file.PendingTask = "" })
			}
			return err
		}
	} else {
		own, err := provider.List(run.ctx, parent, false)
		if err != nil {
			return err
		}
		exists := false
		for _, entry := range own {
			if entry.Name == file.Name {
				if entry.Dir || (entry.Size > 0 && file.SourceSize > 0 && entry.Size != file.SourceSize) {
					return errors.New("转存目录出现不同内容的同名文件，请检查网盘目录")
				}
				exists = true
			}
		}
		if !exists {
			entries, err := provider.List(run.ctx, file.ShareParent, true)
			if err != nil {
				return err
			}
			var source *trackingShareFile
			for _, entry := range entries {
				if (file.ShareID == "" || entry.ID == file.ShareID) && entry.Name == file.Name && !entry.Dir && (entry.Size == 0 || file.SourceSize == 0 || entry.Size == file.SourceSize) {
					value := entry
					source = &value
					break
				}
			}
			if source == nil {
				return errors.New("分享文件已经改变或被移除，请重新搜索资源")
			}
			if err = run.change(index, func(file *cloudTransferFile) { file.PendingParent = parent; file.Remote = remote }); err != nil {
				return err
			}
			task, err := provider.Save(run.ctx, parent, []trackingShareFile{*source})
			if err != nil {
				return err
			}
			if err = run.change(index, func(file *cloudTransferFile) { file.PendingTask = task }); err != nil {
				return err
			}
			if task != "" {
				if err = provider.Wait(run.ctx, task); err != nil {
					return err
				}
			}
		}
	}
	if err = confirm(); err != nil {
		return err
	}
	return run.change(index, func(file *cloudTransferFile) {
		file.PendingTask, file.PendingParent = "", ""
		file.Transferred, file.Remote, file.State, file.Stage = true, remote, "queued", "download"
	})
}

func (run *cloudTransferRun) prepareCache() error {
	directory := cloudTransferCache(run.task.ID)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return errors.New("无法创建任务临时目录，请检查应用数据卷权限")
	}
	resolved, err := filepath.EvalSymlinks(directory)
	if err != nil || resolved != filepath.Clean(directory) {
		return errors.New("任务临时目录不能使用符号链接")
	}
	return nil
}

func transferLocal(task, file, suffix string) string {
	return filepath.Join(cloudTransferCache(task), file+suffix)
}

func transferOutputAllowance(size int64) int64 {
	return size + max(int64(64<<20), size/20)
}

func (run *cloudTransferRun) reserve(file cloudTransferFile) (int64, error) {
	run.mu.Lock()
	defer run.mu.Unlock()
	var used int64
	cached := map[string]int64{}
	err := filepath.WalkDir(cloudTransferCache(run.task.ID), func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("临时文件不能使用符号链接")
		}
		if !entry.IsDir() {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			used += info.Size()
			cached[strings.SplitN(entry.Name(), ".", 2)[0]] += info.Size()
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	if file.SourceSize <= 0 {
		return 0, errors.New("网盘未返回文件大小，无法分配临时空间，请刷新挂载后重试")
	}
	allocation := file.SourceSize + transferOutputAllowance(file.SourceSize)
	if file.Remuxed {
		allocation = cached[file.ID]
	}
	needed := max(int64(0), allocation-cached[file.ID])
	var pending int64
	for id, ceiling := range run.reservations {
		pending += max(int64(0), ceiling-cached[id])
	}
	if used+pending+needed > (int64(run.task.Config.CacheLimitGB) << 30) {
		return 0, errors.New("任务临时空间上限不足；在详情中增大空间上限或减少并发后继续任务")
	}
	var disk unix.Statfs_t
	if err := unix.Statfs(cloudTransferCache(run.task.ID), &disk); err != nil || int64(disk.Bavail)*int64(disk.Bsize) < pending+needed+(64<<20) {
		return 0, errors.New("临时目录所在磁盘的可用空间不足")
	}
	if run.reservations == nil {
		run.reservations = map[string]int64{}
	}
	run.reservations[file.ID] = allocation
	return needed, nil
}

func (run *cloudTransferRun) process(index int, mount cloudMount) error {
	run.mu.Lock()
	file := run.task.Files[index]
	allUploaded := run.allUploaded(file)
	run.mu.Unlock()
	if allUploaded {
		return run.completeFile(index, file)
	}
	var object transferObject
	var err error
	if !file.Remuxed {
		object, err = transferCloudObject(run.ctx, mount, file.Remote)
		if err != nil {
			return err
		}
	}
	if !file.Remuxed && object.Size > 0 {
		if file.SourceSize > 0 && file.SourceSize != object.Size {
			return errors.New("转存后的源文件大小已改变，请检查网盘文件")
		}
		file.SourceSize = object.Size
		if err = run.change(index, func(saved *cloudTransferFile) { saved.SourceSize = object.Size }); err != nil {
			return err
		}
	}
	_, err = run.reserve(file)
	if err != nil {
		return err
	}
	defer func() { run.mu.Lock(); delete(run.reservations, file.ID); run.mu.Unlock() }()
	if !file.Remuxed {
		if err = run.download(index, mount, object); err != nil {
			return err
		}
	}
	if err = run.remux(index); err != nil {
		return err
	}
	for _, target := range run.task.Config.Targets {
		if err = run.upload(index, target); err != nil {
			return err
		}
	}
	return run.completeFile(index, file)
}

func (run *cloudTransferRun) allUploaded(file cloudTransferFile) bool {
	if len(run.task.Config.Targets) == 0 {
		return false
	}
	for _, target := range run.task.Config.Targets {
		if file.Uploads[target.MountID] != "complete" {
			return false
		}
	}
	return true
}

func (run *cloudTransferRun) completeFile(index int, file cloudTransferFile) error {
	var err error
	if !run.task.Config.KeepLocal {
		for _, suffix := range []string{".source", ".download", ".output", ".remux"} {
			if err = os.Remove(transferLocal(run.task.ID, file.ID, suffix)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return errors.New("上传已完成，但清理本任务临时文件失败，请检查目录权限后重试")
			}
		}
	}
	return run.change(index, func(file *cloudTransferFile) {
		file.State, file.Stage, file.Error, file.Bytes = "complete", "complete", "", 0
	})
}

func (run *cloudTransferRun) downloadMountID() string { return "transfer-download-" + run.task.ID }

func (run *cloudTransferRun) findDownloadMount(ctx context.Context) (cloudStorage, error) {
	var list struct {
		Content []cloudStorage `json:"content"`
	}
	err := cloudCall(ctx, "GET", "/api/admin/storage/list?page=1&per_page=1000", nil, &list, "")
	for _, storage := range list.Content {
		if storage.MountPath == cloudStoragePath(cloudMount{ID: run.downloadMountID()}, "/") {
			return storage, err
		}
	}
	return cloudStorage{}, err
}

func (run *cloudTransferRun) prepareDownloadMount(source cloudMount) (cloudMount, error) {
	cloudManageMu.Lock()
	defer cloudManageMu.Unlock()
	storage, err := cloudGetStorage(run.ctx, source.StorageID)
	if err != nil {
		return cloudMount{}, err
	}
	existing, err := run.findDownloadMount(run.ctx)
	if err != nil {
		return cloudMount{}, err
	}
	mount := cloudMount{ID: run.downloadMountID(), Driver: source.Driver, Enabled: true}
	storage.ID, storage.MountPath = existing.ID, cloudStoragePath(mount, "/")
	storage.Disabled, storage.WebProxy, storage.EnableSign = false, true, true
	storage.WebdavPolicy, storage.CacheExpiration = "native_proxy", 0
	var addition map[string]any
	if json.Unmarshal([]byte(storage.Addition), &addition) != nil || addition == nil {
		return mount, errCloudAccount
	}
	if source.Driver == "Quark" {
		addition["use_transcoding_address"] = false
	}
	storage.Addition = featureJSON(addition)
	endpoint := "/api/admin/storage/create"
	if storage.ID != 0 {
		endpoint = "/api/admin/storage/update"
	}
	err = cloudCall(run.ctx, "POST", endpoint, storage, nil, "")
	return mount, err
}

func (run *cloudTransferRun) cleanupDownloadMount() {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cloudManageMu.Lock()
	defer cloudManageMu.Unlock()
	storage, err := run.findDownloadMount(ctx)
	if err == nil && storage.ID != 0 {
		_ = cloudCall(ctx, "POST", "/api/admin/storage/delete?id="+cloudInt(storage.ID), nil, nil, "")
	}
}

func transferStage(ctx context.Context, run *cloudTransferRun, index int, stage string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return run.change(index, func(file *cloudTransferFile) {
		file.State, file.Stage, file.Error, file.Bytes = "running", stage, "", 0
	})
}
