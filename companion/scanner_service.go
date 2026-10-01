package main

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"
)

type concurrentScanStatus struct {
	ID          string `json:"Id"`
	Status      string
	Count       int
	Concurrency int
}

type concurrentScanState struct {
	mu     sync.Mutex
	ctx    context.Context
	cancel context.CancelFunc
	locks  *sql.DB
	active map[string]concurrentScanStatus
	wg     sync.WaitGroup
}

func (a *App) initConcurrentScanner() error {
	locks, err := sql.Open("postgres", os.Getenv("DATABASE_URL"))
	if err != nil {
		return err
	}
	// Library row locks use their own pool, leaving the normal pool available
	// for catalog writes. NO KEY UPDATE also permits the items' FK checks.
	locks.SetMaxOpenConns(64)
	locks.SetMaxIdleConns(2)
	locks.SetConnMaxIdleTime(time.Minute)
	a.scanner.ctx, a.scanner.cancel = context.WithCancel(context.Background())
	a.scanner.locks = locks
	a.scanner.active = make(map[string]concurrentScanStatus)
	return nil
}

func (a *App) scanFileConcurrency() int {
	var raw string
	_ = a.db.QueryRow("SELECT v FROM settings WHERE k='scan_file_concurrency'").Scan(&raw)
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > 32 {
		return 8
	}
	return n
}

func (a *App) scanSettingsAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		respond(w, M{"FileConcurrency": a.scanFileConcurrency()})
		return
	}
	if r.Method != http.MethodPut {
		fail(w, 405, "GET or PUT required")
		return
	}
	var config struct{ FileConcurrency *int }
	if !body(w, r, &config) {
		return
	}
	if config.FileConcurrency == nil || *config.FileConcurrency < 1 || *config.FileConcurrency > 32 {
		fail(w, 400, "单库扫描并发必须为 1–32 的整数")
		return
	}
	if _, err := a.db.Exec("INSERT INTO settings(k,v) VALUES('scan_file_concurrency',?) ON CONFLICT(k) DO UPDATE SET v=excluded.v", strconv.Itoa(*config.FileConcurrency)); err != nil {
		fail(w, 500, "保存扫描并发失败")
		return
	}
	a.wakeLibraryJobs()
	respond(w, M{"FileConcurrency": *config.FileConcurrency})
}

func (a *App) reserveConcurrentScan(lib string) (string, bool) {
	a.scanner.mu.Lock()
	defer a.scanner.mu.Unlock()
	if a.scanner.ctx == nil || a.scanner.ctx.Err() != nil {
		return "", false
	}
	if _, exists := a.scanner.active[lib]; exists {
		return "", false
	}
	a.scanner.active[lib] = concurrentScanStatus{ID: lib, Status: "queued", Concurrency: a.scanFileConcurrency()}
	a.scanner.wg.Add(1)
	return lib, true
}

func (a *App) finishConcurrentScan(lib string) {
	a.scanner.mu.Lock()
	delete(a.scanner.active, lib)
	a.scanner.mu.Unlock()
	a.scanner.wg.Done()
}

func (a *App) concurrentScanStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		fail(w, 405, "GET required")
		return
	}
	a.scanner.mu.Lock()
	statuses := make([]concurrentScanStatus, 0, len(a.scanner.active))
	for _, status := range a.scanner.active {
		statuses = append(statuses, status)
	}
	a.scanner.mu.Unlock()
	respond(w, M{"Libraries": statuses})
}

func (a *App) concurrentScanAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		fail(w, 405, "POST required")
		return
	}
	var request struct{ ID, Mode string }
	if !body(w, r, &request) {
		return
	}
	if request.Mode != "" && request.Mode != "scan" && request.Mode != "update" {
		fail(w, 400, "无效扫描模式")
		return
	}
	ids := []string{}
	if r.URL.Path == "/admin/scan-all" {
		rows, err := a.db.Query("SELECT id FROM libraries ORDER BY id")
		if err != nil {
			fail(w, 500, "读取媒体库失败")
			return
		}
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				rows.Close()
				fail(w, 500, "读取媒体库失败")
				return
			}
			ids = append(ids, id)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			fail(w, 500, "读取媒体库失败")
			return
		}
		if request.Mode == "" {
			request.Mode = "update"
		}
	} else {
		var id string
		if err := a.db.QueryRow("SELECT id FROM libraries WHERE id=?", request.ID).Scan(&id); err != nil {
			if err == sql.ErrNoRows {
				fail(w, 404, "媒体库不存在")
			} else {
				fail(w, 500, "读取媒体库失败")
			}
			return
		}
		ids = append(ids, id)
	}
	queued := 0
	for _, lib := range ids {
		if _, reserved := a.reserveConcurrentScan(lib); reserved {
			queued++
			go a.runConcurrentScan(lib, request.Mode == "update", false, nil)
		}
	}
	respond(w, M{"queued": true, "Queued": queued, "Message": "扫描任务已加入队列"})
}

func (a *App) waitConcurrentScan(ctx context.Context, job string) error {
	a.scanControlMu.Lock()
	paused := a.scanResume
	a.scanControlMu.Unlock()
	if paused == nil {
		return ctx.Err()
	}
	previous := "running"
	a.changeActivity(job, func(entry *activityEntry) { previous = entry.State; entry.State = "paused" })
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-paused:
		a.changeActivity(job, func(entry *activityEntry) { entry.State = previous })
		return ctx.Err()
	}
}

func (a *App) lockScanLibrary(ctx context.Context, lib string) (*sql.Tx, string, string, string, error) {
	for {
		tx, err := a.scanner.locks.BeginTx(ctx, nil)
		if err != nil {
			return nil, "", "", "", err
		}
		var root, kind, name, status string
		err = tx.QueryRowContext(ctx, "SELECT path,kind,name,status FROM libraries WHERE id=$1 FOR NO KEY UPDATE", lib).Scan(&root, &kind, &name, &status)
		if err != nil {
			tx.Rollback()
			return nil, "", "", "", err
		}
		if status != "scanning" {
			return tx, root, kind, name, nil
		}
		// The original scanner is already working. Wait without holding its row.
		tx.Rollback()
		timer := time.NewTimer(500 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, "", "", "", ctx.Err()
		case <-timer.C:
		}
	}
}

func (a *App) stopConcurrentScanner(ctx context.Context) error {
	a.scanner.mu.Lock()
	if a.scanner.cancel != nil {
		a.scanner.cancel()
	}
	a.scanner.mu.Unlock()
	// Wake old queue users; every newly dispatched task observes cancellation.
	a.scanControlMu.Lock()
	if a.scanResume != nil {
		close(a.scanResume)
		a.scanResume = nil
	}
	a.scanControlMu.Unlock()
	done := make(chan struct{})
	go func() { a.scanner.wg.Wait(); close(done) }()
	select {
	case <-done:
		return a.scanner.locks.Close()
	case <-ctx.Done():
		a.scanner.locks.Close()
		return errors.New("扫描服务关闭超时")
	}
}
