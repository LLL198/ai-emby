package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Downloads wait at read boundaries; media tools are suspended in place so continuing keeps their output.
type playbackTaskControl struct {
	mu      sync.Mutex
	paused  bool
	stopped bool
	changed chan struct{}
	process *os.Process
}

func newPlaybackTaskControl() *playbackTaskControl {
	return &playbackTaskControl{changed: make(chan struct{})}
}

func (control *playbackTaskControl) wait(ctx context.Context) error {
	if control == nil {
		return ctx.Err()
	}
	for {
		control.mu.Lock()
		paused, stopped, changed := control.paused, control.stopped, control.changed
		control.mu.Unlock()
		if stopped {
			return context.Canceled
		}
		if !paused {
			return ctx.Err()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		}
	}
}

func (control *playbackTaskControl) isPaused() bool {
	if control == nil {
		return false
	}
	control.mu.Lock()
	defer control.mu.Unlock()
	return control.paused
}

// Pausing a task must not consume its probing or directory-reading time budget.
func playbackTaskTimeout(parent context.Context, control *playbackTaskControl, duration time.Duration) (context.Context, context.CancelFunc) {
	if control == nil {
		return context.WithTimeout(parent, duration)
	}
	ctx, cancel := context.WithCancel(parent)
	go func() {
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		last := time.Now()
		remaining := duration
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				if !control.isPaused() {
					remaining -= now.Sub(last)
				}
				last = now
				if remaining <= 0 {
					cancel()
					return
				}
			}
		}
	}()
	return ctx, cancel
}

func (control *playbackTaskControl) change(action string) error {
	control.mu.Lock()
	defer control.mu.Unlock()
	if control.stopped {
		return nil
	}
	signal := syscall.SIGCONT
	if action == "pause" {
		signal = syscall.SIGSTOP
	}
	if control.process != nil {
		if err := control.process.Signal(signal); err != nil && !errors.Is(err, os.ErrProcessDone) && !errors.Is(err, syscall.ESRCH) {
			return err
		}
	}
	control.paused = action == "pause"
	control.stopped = action == "stop"
	close(control.changed)
	control.changed = make(chan struct{})
	return nil
}

func (control *playbackTaskControl) run(ctx context.Context, command *exec.Cmd) error {
	if control == nil {
		return command.Run()
	}
	for {
		if err := control.wait(ctx); err != nil {
			return err
		}
		control.mu.Lock()
		if control.paused {
			control.mu.Unlock()
			continue
		}
		if control.stopped || ctx.Err() != nil {
			control.mu.Unlock()
			return context.Canceled
		}
		if err := command.Start(); err != nil {
			control.mu.Unlock()
			return err
		}
		control.process = command.Process
		control.mu.Unlock()
		err := command.Wait()
		control.mu.Lock()
		control.process = nil
		control.mu.Unlock()
		return err
	}
}

type playbackControlledReader struct {
	ctx     context.Context
	reader  io.Reader
	control *playbackTaskControl
}

func (reader *playbackControlledReader) Read(data []byte) (int, error) {
	if err := reader.control.wait(reader.ctx); err != nil {
		return 0, err
	}
	return reader.reader.Read(data)
}

func (a *App) featurePlaybackControl(w http.ResponseWriter, r *http.Request, user User) {
	if !featureMethod(w, r, http.MethodPost) {
		return
	}
	if user.API {
		fail(w, 403, "需要登录账号")
		return
	}
	var request struct{ ID, Action string }
	if !body(w, r, &request) {
		return
	}
	if request.Action != "pause" && request.Action != "resume" && request.Action != "stop" && request.Action != "delete" {
		fail(w, 400, "不支持的播放任务操作")
		return
	}
	a.features.cacheMu.Lock()
	job := a.features.transcodes[request.ID]
	// A stopped service may leave a cache record with no running process.
	if job == nil && request.Action == "delete" && len(request.ID) == 40 && strings.Trim(request.ID, "0123456789abcdef") == "" {
		var record featureTranscode
		data, err := os.ReadFile(filepath.Join(featureDataRoot(), "playback", request.ID, "record.json"))
		if err == nil && json.Unmarshal(data, &record) == nil {
			record.ID, record.Directory, record.Done = request.ID, filepath.Join(featureDataRoot(), "playback", request.ID), true
			job = &record
		}
	}
	if job == nil || (job.Owner != user.ID && !user.Admin) {
		a.features.cacheMu.Unlock()
		fail(w, 404, "播放任务不存在")
		return
	}
	item := job.Item
	a.features.cacheMu.Unlock()
	if request.Action == "pause" || request.Action == "resume" {
		if _, err := a.featureAccessibleItem(user, item); err != nil {
			featureError(w, err)
			return
		}
		if !user.Admin && !a.canPlay(user.ID) {
			fail(w, 403, "该账号没有播放权限")
			return
		}
	}
	a.features.cacheMu.Lock()
	defer a.features.cacheMu.Unlock()
	if a.features.transcodes[request.ID] == nil && request.Action == "delete" {
		a.features.transcodes[request.ID] = job
	}
	if a.features.transcodes[request.ID] != job {
		fail(w, 404, "播放任务不存在")
		return
	}
	if request.Action == "delete" {
		if !job.deleting {
			if err := a.deletePlaybackTaskLocked(job); err != nil {
				fail(w, 502, "无法停止并删除播放任务，请稍后重试")
				return
			}
		}
		respond(w, M{"ID": job.ID, "Deleting": true})
		return
	}
	if job.deleting {
		fail(w, 409, "此播放任务正在删除")
		return
	}
	if !job.Done {
		if job.control == nil {
			fail(w, 409, "此任务暂不支持控制，请重新启动播放")
			return
		}
		if err := job.control.change(request.Action); err != nil {
			fail(w, 502, "无法控制播放任务，请稍后重试")
			return
		}
		job.Paused = request.Action == "pause"
		if request.Action == "stop" {
			job.State = "stopping"
			if job.cancel != nil {
				job.cancel()
			}
		}
		job.Used = time.Now()
	}
	respond(w, M{"ID": job.ID, "State": job.State, "Done": job.Done, "Paused": job.Paused})
}

// Hold cacheMu while scheduling deletion. Keep the job registered until all tools
// and private ISO readers exit, so a retry cannot recreate files being removed.
func (a *App) deletePlaybackTaskLocked(job *featureTranscode) error {
	if len(job.ID) != 40 || strings.Trim(job.ID, "0123456789abcdef") != "" {
		return errors.New("播放任务编号无效")
	}
	if !job.Done {
		if job.control != nil {
			if err := job.control.change("stop"); err != nil {
				return err
			}
		}
		if job.cancel == nil || job.finished == nil {
			return errors.New("任务无法安全停止")
		}
		job.cancel()
	}
	job.deleting, job.Paused, job.State = true, false, "deleting"
	job.deleted = make(chan struct{})
	a.features.wg.Add(1)
	go func() {
		defer a.features.wg.Done()
		defer close(job.deleted)
		if job.finished != nil {
			<-job.finished
		}
		a.features.cacheMu.Lock()
		defer a.features.cacheMu.Unlock()
		if a.features.transcodes[job.ID] != job {
			return
		}
		// Ignore Directory from persisted records and derive the validated cache path.
		if err := os.RemoveAll(filepath.Join(featureDataRoot(), "playback", job.ID)); err != nil {
			job.deleting, job.State, job.Error = false, "delete-failed", "删除缓存失败，请重试"
			return
		}
		delete(a.features.transcodes, job.ID)
	}()
	return nil
}
