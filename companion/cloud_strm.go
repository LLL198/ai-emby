package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
)

type cloudGenerateRequest struct {
	ID, Source, Output, PublicURL, Library string
	Recursive, Overwrite                   bool
	Limit, Concurrency                     int
	localPath                              func(context.Context, string, string) (string, error)
}

var cloudOutputs = map[string]string{}
var cloudVideos = map[string]bool{".mkv": true, ".mp4": true, ".avi": true, ".mov": true, ".wmv": true, ".m4v": true, ".ts": true, ".m2ts": true, ".flv": true, ".webm": true, ".mpg": true, ".mpeg": true, ".rmvb": true, ".iso": true}

func cloudOutput(p string) (string, error) {
	root := filepath.Clean(fileRoot())
	p = filepath.Clean(p)
	if !filepath.IsAbs(p) || (p != root && !strings.HasPrefix(p, root+string(filepath.Separator))) {
		return "", errors.New("输出目录必须在媒体目录内")
	}
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return "", err
	}
	return fileName(rel)
}
func (a *App) cloudGenerateAPI(w http.ResponseWriter, r *http.Request) {
	if !featureMethod(w, r, "POST") {
		return
	}
	var b cloudGenerateRequest
	if !body(w, r, &b) {
		return
	}
	source, err := cloudPath(b.Source)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	b.Source = source
	output, err := cloudOutput(b.Output)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	b.Output = filepath.Join(fileRoot(), output)
	u, err := url.Parse(strings.TrimSpace(b.PublicURL))
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") {
		fail(w, 400, "请填写播放器可访问的 AI Emby 服务地址")
		return
	}
	b.PublicURL = strings.TrimSuffix(u.String(), "/")
	if b.Limit < 0 || b.Limit > 100000 || b.Concurrency < 1 || b.Concurrency > 8 {
		fail(w, 400, "数量限制为0–100000，生成并发为1–8")
		return
	}
	if b.Library != "" {
		found := false
		for _, lib := range a.libraries() {
			if lib["Id"] == b.Library {
				for _, root := range lib["Locations"].([]string) {
					rel, e := filepath.Rel(root, b.Output)
					if e == nil && (rel == "." || filepath.IsLocal(rel)) {
						found = true
					}
				}
			}
		}
		if !found {
			fail(w, 400, "输出目录需要位于所选媒体库内")
			return
		}
	}
	cloudManageMu.Lock()
	defer cloudManageMu.Unlock()
	m, err := a.cloudMount(b.ID)
	if err != nil {
		featureError(w, err)
		return
	}
	if !m.Enabled {
		fail(w, 409, "挂载已暂停")
		return
	}
	for _, p := range cloudOutputs {
		if pathsOverlap(p, b.Output) {
			fail(w, 409, "这个输出目录已有生成任务")
			return
		}
	}
	if a.cloudJobRunning(m.ID) {
		fail(w, 409, "此挂载已有生成任务")
		return
	}
	root, err := os.OpenRoot(fileRoot())
	if err != nil {
		fileFail(w, err)
		return
	}
	err = cloudMakeDirectory(root, output)
	root.Close()
	if err != nil {
		fileFail(w, err)
		return
	}
	ctx, cancel := context.WithCancel(a.features.ctx)
	key := "cloud:" + m.ID
	a.features.mu.Lock()
	a.features.jobs[key] = cancel
	a.features.mu.Unlock()
	cloudOutputs[m.ID] = b.Output
	job := a.newActivity("cloud-strm", m.ID, "生成 STRM · "+m.Name)
	a.features.wg.Add(1)
	go func() {
		defer a.features.wg.Done()
		defer cancel()
		defer func() {
			cloudManageMu.Lock()
			delete(cloudOutputs, m.ID)
			a.features.mu.Lock()
			delete(a.features.jobs, key)
			a.features.mu.Unlock()
			cloudManageMu.Unlock()
		}()
		err := a.cloudGenerate(ctx, m, b, job)
		a.finishActivity(job, err)
		if err == nil && ctx.Err() == nil && b.Library != "" {
			if _, ok := a.reserveConcurrentScan(b.Library); ok {
				go a.runConcurrentScan(b.Library, true, false, nil)
				a.changeActivity(job, func(e *activityEntry) { e.Current += " · 已提交媒体库扫描" })
			} else {
				a.changeActivity(job, func(e *activityEntry) { e.Current += " · 媒体库正在扫描，完成后请手动刷新" })
			}
		}
	}()
	respond(w, M{"ID": job, "Queued": true})
}

func cloudMakeDirectory(root *os.Root, dir string) error {
	if dir == "." {
		return nil
	}
	parts := strings.Split(filepath.ToSlash(dir), "/")
	for i := range parts {
		p := strings.Join(parts[:i+1], "/")
		fi, err := fileCheck(root, p)
		if errors.Is(err, fs.ErrNotExist) {
			if err = root.Mkdir(p, 0755); err != nil && !errors.Is(err, fs.ErrExist) {
				return err
			}
			fi, err = fileCheck(root, p)
		}
		if err != nil {
			return err
		}
		if !fi.IsDir() {
			return errors.New("输出路径包含非目录文件")
		}
	}
	return nil
}
func cloudLocalName(name string) (string, error) {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\\x00\r\n") {
		return "", errors.New("网盘包含不合法文件名")
	}
	var b strings.Builder
	for _, r := range name {
		if strings.ContainsRune(`%:*?"<>|`, r) {
			fmt.Fprintf(&b, "%%%02X", r)
		} else {
			b.WriteRune(r)
		}
	}
	return b.String(), nil
}
func cloudWriteSTRM(root *os.Root, output, content string, overwrite bool) (bool, error) {
	if err := cloudMakeDirectory(root, filepath.Dir(output)); err != nil {
		return false, err
	}
	fi, err := fileCheck(root, output)
	if err == nil {
		if !fi.Mode().IsRegular() {
			return false, errors.New("输出文件不是普通文件")
		}
		if !overwrite {
			return false, nil
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return false, err
	}
	if !overwrite {
		f, err := root.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
		if errors.Is(err, fs.ErrExist) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		_, writeErr := f.WriteString(content + "\n")
		closeErr := f.Close()
		if writeErr != nil || closeErr != nil {
			root.Remove(output)
			return false, errors.Join(writeErr, closeErr)
		}
		return true, nil
	}
	tmp := filepath.Join(filepath.Dir(output), ".strm-"+id()+".tmp")
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return false, err
	}
	defer root.Remove(tmp)
	_, writeErr := f.WriteString(content + "\n")
	closeErr := f.Close()
	if writeErr != nil || closeErr != nil {
		return false, errors.Join(writeErr, closeErr)
	}
	if err = root.Rename(tmp, output); err != nil {
		return false, err
	}
	return true, nil
}

func (a *App) cloudGenerate(ctx context.Context, m cloudMount, b cloudGenerateRequest, job string) error {
	root, err := os.OpenRoot(fileRoot())
	if err != nil {
		return errors.New("无法打开输出目录")
	}
	defer root.Close()
	output, _ := cloudOutput(b.Output)
	type work struct{ remote, local string }
	queue := make(chan work, b.Concurrency*2)
	var wg sync.WaitGroup
	var mu sync.Mutex
	done, written, skipped, failed, renamed, total := 0, 0, 0, 0, 0, 0
	firstError := ""
	update := func(current string) {
		mu.Lock()
		defer mu.Unlock()
		a.changeActivity(job, func(e *activityEntry) {
			e.State = "running"
			e.Total = total
			e.Done = done
			e.Current = fmt.Sprintf("已生成 %d · 已跳过 %d · 失败 %d · %s", written, skipped, failed, current)
		})
	}
	for i := 0; i < b.Concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for item := range queue {
				if ctx.Err() != nil {
					return
				}
				query := url.Values{"path": {item.remote}, "sign": {cloudSign(m, item.remote)}}
				link := b.PublicURL + "/cloud/resolve/" + m.ID + "?" + query.Encode()
				ok, e := cloudWriteSTRM(root, filepath.Join(output, item.local+".strm"), link, b.Overwrite)
				mu.Lock()
				done++
				if e != nil {
					failed++
					if firstError == "" {
						firstError = "写入失败：" + item.local
					}
				} else if ok {
					written++
				} else {
					skipped++
				}
				mu.Unlock()
				update(item.local)
			}
		}()
	}
	limit := b.Limit
	if limit == 0 {
		limit = 100000
	}
	visited, dirs, found := 0, 0, 0
	var walk func(string, string, int) error
	walk = func(remote, local string, depth int) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if depth > 64 {
			return errors.New("目录层级超过64，请缩小生成范围")
		}
		dirs++
		if dirs > 20000 {
			return errors.New("目录数量超过20000，请缩小生成范围")
		}
		for page := 1; page <= 2500; page++ {
			if found >= limit {
				return nil
			}
			list, e := cloudList(ctx, m, remote, page, false)
			if e != nil {
				return fmt.Errorf("读取目录 %s：%w", remote, e)
			}
			for _, entry := range list.Content {
				if found >= limit {
					return nil
				}
				visited++
				if visited > 500000 {
					return errors.New("文件数量过大，请缩小生成范围")
				}
				name, e := cloudLocalName(entry.Name)
				if e != nil {
					return e
				}
				if name != entry.Name {
					renamed++
				}
				childRemote := path.Join(remote, entry.Name)
				childLocal := path.Join(local, name)
				if entry.IsDir {
					if b.Recursive {
						if e := walk(childRemote, childLocal, depth+1); e != nil {
							return e
						}
					}
					continue
				}
				if !cloudVideos[strings.ToLower(path.Ext(entry.Name))] {
					continue
				}
				found++
				if b.localPath != nil {
					childLocal, e = b.localPath(ctx, childRemote, childLocal)
					if e != nil {
						return e
					}
				}
				mu.Lock()
				total = found
				mu.Unlock()
				update(childLocal)
				select {
				case queue <- work{childRemote, childLocal}:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			if page*200 >= list.Total {
				break
			}
			if len(list.Content) == 0 {
				return errors.New("网盘目录分页中断，请刷新后重试")
			}
			if page == 2500 {
				return errors.New("目录分页超过限制，请缩小生成范围")
			}
		}
		return nil
	}
	err = walk(b.Source, "", 0)
	close(queue)
	wg.Wait()
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	if err == nil && failed > 0 {
		err = fmt.Errorf("%d 个文件写入失败；%s", failed, firstError)
	}
	if err == nil && found == 0 {
		err = errors.New("所选目录没有找到视频文件")
	}
	a.changeActivity(job, func(e *activityEntry) {
		e.Done = done
		e.Total = total
		e.Current = fmt.Sprintf("生成 %d · 跳过 %d · 失败 %d · 文件名转义 %d · %s", written, skipped, failed, renamed, b.Output)
		if found >= limit {
			e.Current += " · 已达到数量上限"
		}
	})
	return err
}

func (a *App) cloudTasks() []activityEntry {
	tasks := []activityEntry{}
	seen := map[string]bool{}
	a.activity.mu.Lock()
	for i := len(a.activity.entries) - 1; i >= 0; i-- {
		e := a.activity.entries[i]
		if e.Category == "cloud-strm" {
			tasks = append(tasks, *e)
			seen[e.ID] = true
		}
	}
	a.activity.mu.Unlock()
	rows, err := a.db.Query("SELECT data,state FROM feature_tasks WHERE category='cloud-strm' ORDER BY updated DESC LIMIT 20")
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var data, state string
			if rows.Scan(&data, &state) != nil {
				continue
			}
			var e activityEntry
			if json.Unmarshal([]byte(data), &e) == nil && !seen[e.ID] {
				e.State = state
				tasks = append(tasks, e)
			}
		}
	}
	if len(tasks) > 20 {
		tasks = tasks[:20]
	}
	return tasks
}
