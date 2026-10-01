package main

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type featurePhase struct {
	Name     string
	Started  time.Time
	Finished time.Time
	Total    int
	Done     int
	Error    string
}
type featureTask struct {
	activityEntry
	Phases []featurePhase
	Failed int
	Source string
}

var featureFailureCount = regexp.MustCompile(`失败\s+(\d+)`)

func featureTaskCategory(category string) bool {
	return category == "scan" || category == "update" || category == "probe" || category == "scraper" || strings.HasPrefix(category, "library-") || category == "import" || category == "cover" || category == "cloud-strm"
}

func (a *App) featurePersistActivities() {
	if a.features.ctx == nil {
		return
	}
	a.activity.mu.Lock()
	entries := make([]activityEntry, 0, len(a.activity.entries))
	for _, entry := range a.activity.entries {
		if featureTaskCategory(entry.Category) {
			entries = append(entries, *entry)
		}
	}
	a.activity.mu.Unlock()
	for _, entry := range entries {
		_ = a.featureSaveActivity(entry, "worker")
	}
}

func (a *App) featureSaveActivity(entry activityEntry, source string) error {
	var task featureTask
	var previous string
	storedID := source + ":" + entry.ID
	if a.db.QueryRow("SELECT data FROM feature_tasks WHERE id=?", storedID).Scan(&previous) == nil {
		_ = json.Unmarshal([]byte(previous), &task)
		if task.Updated.Equal(entry.Updated) && task.State == entry.State {
			return nil
		}
	}
	phase := entry.State
	if strings.HasPrefix(entry.Current, "读取") || entry.State == "counting" {
		phase = "读取目录"
	} else if entry.State == "running" {
		phase = "处理文件"
	} else if entry.State == "cleaning" {
		phase = "整理索引"
	}
	terminal := entry.State == "complete" || entry.State == "error" || entry.State == "cancelled"
	if len(task.Phases) == 0 || (task.Phases[len(task.Phases)-1].Name != phase && !terminal) {
		if len(task.Phases) > 0 {
			task.Phases[len(task.Phases)-1].Finished = entry.Updated
		}
		if len(task.Phases) < 32 {
			task.Phases = append(task.Phases, featurePhase{Name: phase, Started: entry.Updated})
		}
	}
	if len(task.Phases) > 0 {
		p := &task.Phases[len(task.Phases)-1]
		p.Total = entry.Total
		p.Done = entry.Done
		if terminal {
			p.Finished = entry.Updated
			p.Error = entry.Error
		}
	}
	task.activityEntry = entry
	task.Source = source
	if entry.Error != "" {
		task.Failed = max(task.Failed, 1)
	}
	if match := featureFailureCount.FindStringSubmatch(entry.Current); len(match) == 2 {
		task.Failed, _ = strconv.Atoi(match[1])
	}
	_, err := a.db.Exec("INSERT INTO feature_tasks(id,category,item,state,data,started,updated) VALUES(?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET state=excluded.state,data=excluded.data,updated=excluded.updated WHERE feature_tasks.updated<=excluded.updated", storedID, entry.Category, entry.ItemID, entry.State, featureJSON(task), entry.Started.UnixNano(), entry.Updated.UnixNano())
	return err
}

func (a *App) featureImportActivities(w http.ResponseWriter, r *http.Request) {
	if !featureMethod(w, r, http.MethodPost) {
		return
	}
	var request struct{ Entries []activityEntry }
	if !body(w, r, &request) {
		return
	}
	if len(request.Entries) > 1000 {
		fail(w, 400, "记录过多")
		return
	}
	for _, entry := range request.Entries {
		if entry.ID != "" && featureTaskCategory(entry.Category) {
			if err := a.featureSaveActivity(entry, "core"); err != nil {
				featureError(w, err)
				return
			}
		}
	}
	respond(w, M{"ok": true})
}

func (a *App) featureTasksAPI(w http.ResponseWriter, r *http.Request) {
	if !featureMethod(w, r, http.MethodGet) {
		return
	}
	a.featurePersistActivities()
	limit := featureLimit(r, 100, 500)
	rows, err := a.db.Query("SELECT id,data,state FROM feature_tasks ORDER BY updated DESC LIMIT ?", limit)
	if err != nil {
		featureError(w, err)
		return
	}
	defer rows.Close()
	tasks := []M{}
	counts := map[string]int{}
	filter := r.URL.Query().Get("State")
	for rows.Next() {
		var key, raw, state string
		if err = rows.Scan(&key, &raw, &state); err != nil {
			featureError(w, err)
			return
		}
		var task featureTask
		if json.Unmarshal([]byte(raw), &task) != nil {
			continue
		}
		if task.Category == "scraper" && task.TaskID != "" && task.TaskID != task.ItemID {
			continue
		}
		if task.Category == "probe" && task.Name != "批量提取媒体信息" {
			continue
		}
		task.State = state
		counts[state]++
		active := state == "running" || state == "queued" || state == "waiting" || state == "paused" || state == "counting" || state == "cleaning"
		problem := state == "error" || state == "interrupted" || state == "cancelled" || task.Failed > 0
		if filter == "active" && !active || filter == "problem" && !problem {
			continue
		}
		tasks = append(tasks, M{"ID": key, "Entry": task, "Active": active, "Duration": max(0, task.Updated.Sub(task.Started).Seconds())})
	}
	if err = rows.Err(); err != nil {
		featureError(w, err)
		return
	}
	respond(w, M{"Items": tasks, "Counts": counts})
}

func (a *App) featureTaskAction(w http.ResponseWriter, r *http.Request) {
	if !featureMethod(w, r, http.MethodPost) {
		return
	}
	var request struct{ ID, Action string }
	if !body(w, r, &request) {
		return
	}
	var raw, state string
	if err := a.db.QueryRow("SELECT data,state FROM feature_tasks WHERE id=?", request.ID).Scan(&raw, &state); err != nil {
		featureError(w, err)
		return
	}
	var task featureTask
	if json.Unmarshal([]byte(raw), &task) != nil {
		fail(w, 500, "任务记录无效")
		return
	}
	if task.Source != "worker" {
		fail(w, 409, "此任务由核心服务管理，请使用对应模块的任务控制")
		return
	}
	if request.Action == "retry" {
		if state != "error" && state != "interrupted" && state != "cancelled" && state != "complete" {
			fail(w, 409, "任务仍在运行")
			return
		}
		if task.Category == "scan" || task.Category == "update" {
			if _, ok := a.reserveConcurrentScan(task.ItemID); !ok {
				fail(w, 409, "这个库已有扫描任务")
				return
			}
			go a.runConcurrentScan(task.ItemID, task.Category == "update", false, nil)
		} else if strings.HasPrefix(task.Category, "library-") {
			if _, err := a.featureStartLibraryJob(task.ItemID, strings.TrimPrefix(task.Category, "library-"), token(r), r.UserAgent()); err != nil {
				fail(w, 409, err.Error())
				return
			}
		} else {
			fail(w, 409, "请在刮削模块重新生成计划")
			return
		}
	} else if request.Action == "cancel" {
		key := task.ItemID + ":" + strings.TrimPrefix(task.Category, "library-")
		if task.Category == "import" {
			key = "import:" + task.ID
		}
		if task.Category == "cloud-strm" {
			key = "cloud:" + task.ItemID
		}
		a.features.mu.Lock()
		cancel := a.features.jobs[key]
		a.features.mu.Unlock()
		if cancel == nil {
			fail(w, 409, "此任务不支持单独取消，请在对应模块控制")
			return
		}
		cancel()
	} else {
		fail(w, 400, "无效任务操作")
		return
	}
	respond(w, M{"ok": true})
}
