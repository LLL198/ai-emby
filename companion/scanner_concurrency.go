package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lib/pq"
)

type parsedScanItem struct {
	Item     Item
	Metadata sidecar
}
type scanWork struct {
	File           scanFile
	Skip, ReuseURL bool
	URL            string
}
type scanResult struct {
	Path  string
	Items []parsedScanItem
	Err   error
}
type scanExisting struct {
	URL   string
	Stamp fileStamp
}

func (a *App) existingScanFiles(ctx context.Context, files []scanFile) (map[string]scanExisting, error) {
	ids := make([]string, len(files))
	for index, file := range files {
		ids[index] = digest(file.Path)[:32]
	}
	rows, err := a.db.DB.QueryContext(ctx, "SELECT id,url,mtime,size FROM items WHERE id=ANY($1)", pq.Array(ids))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	existing := make(map[string]scanExisting, len(files))
	for rows.Next() {
		var id, url string
		var mtime, size int64
		if err := rows.Scan(&id, &url, &mtime, &size); err != nil {
			return nil, err
		}
		existing[id] = scanExisting{URL: url, Stamp: fileStamp{Size: size, Mtime: mtime}}
	}
	return existing, rows.Err()
}

func (a *App) dispatchScanFiles(ctx context.Context, jobs chan<- scanWork, inventory *scanInventory, previous fileSnapshot, dirty map[string]bool, incremental bool) error {
	defer close(jobs)
	for offset := 0; offset < len(inventory.Files); offset += 256 {
		end := offset + 256
		if end > len(inventory.Files) {
			end = len(inventory.Files)
		}
		files := inventory.Files[offset:end]
		existing := map[string]scanExisting{}
		if incremental {
			var err error
			existing, err = a.existingScanFiles(ctx, files)
			if err != nil {
				return err
			}
		}
		for _, file := range files {
			old, exists := existing[digest(file.Path)[:32]]
			unchanged := incremental && exists && previous[file.Path] == file.Stamp && old.Stamp == file.Stamp
			work := scanWork{File: file, Skip: unchanged && !dependencyChanged(file.Path, dirty), ReuseURL: unchanged, URL: old.URL}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case jobs <- work:
			}
		}
	}
	return nil
}

func parseScanFile(cache *scanCache, lib, kind string, work scanWork) scanResult {
	result := scanResult{Path: work.File.Path}
	if work.Skip {
		return result
	}
	file := work.File
	item := Item{ID: digest(file.Path)[:32], Lib: lib, Parent: lib, Name: strings.TrimSuffix(filepath.Base(file.Path), filepath.Ext(file.Path)), Kind: "Movie", Path: file.Path, Mtime: file.Stamp.Mtime, Size: file.Stamp.Size}
	if kind == "tvshows" {
		item.Kind = "Episode"
		if match := epPattern.FindStringSubmatch(item.Name); len(match) == 3 {
			item.Season, _ = strconv.Atoi(match[1])
			item.Episode, _ = strconv.Atoi(match[2])
		}
		if relative, _ := filepath.Rel(file.Root, filepath.Dir(file.Path)); relative != "." {
			seriesPath := cache.seriesDirectory(file.Root, file.Path)
			series := cache.parent(lib, lib, seriesPath, "Series")
			result.Items = append(result.Items, series)
			item.Parent = series.Item.ID
			if seasonPath := filepath.Dir(file.Path); seasonPath != seriesPath {
				season := cache.parent(lib, series.Item.ID, seasonPath, "Season")
				result.Items = append(result.Items, season)
				item.Parent = season.Item.ID
				if item.Season == 0 {
					item.Season = season.Item.Season
				}
			}
		}
	}
	if work.ReuseURL {
		item.URL = work.URL
	} else {
		reader, err := os.Open(file.Path)
		if err != nil {
			result.Err = fmt.Errorf("读取 STRM 失败：%s：%w", file.Path, err)
			return result
		}
		data, err := io.ReadAll(io.LimitReader(reader, 65537))
		reader.Close()
		if err != nil {
			result.Err = fmt.Errorf("读取 STRM 失败：%s：%w", file.Path, err)
			return result
		}
		if len(data) > 65536 {
			result.Err = fmt.Errorf("STRM 超过 64KB：%s", file.Path)
			return result
		}
		for _, line := range strings.Split(strings.TrimPrefix(string(data), "\ufeff"), "\n") {
			line = strings.TrimSpace(line)
			if line != "" && !strings.HasPrefix(line, "#") {
				item.URL = line
				break
			}
		}
	}
	cache.applyInfo(&item)
	result.Items = append(result.Items, parsedScanItem{Item: item, Metadata: cache.sidecar(item)})
	return result
}

func (a *App) runConcurrentScan(lib string, incremental, allowEmpty bool, scopes []string) {
	defer a.finishConcurrentScan(lib)
	category := "scan"
	if incremental || len(scopes) > 0 {
		category = "update"
	}
	job := a.newActivity(category, lib, "扫描媒体库")
	var scanErr error
	defer func() { a.finishActivity(job, scanErr) }()
	release := a.acquireLibraryJob(lib, incremental || len(scopes) > 0)
	defer release()
	ctx, cancel := context.WithCancel(a.scanner.ctx)
	defer cancel()
	workerCtx, stopWorkers := context.WithCancel(ctx)
	defer stopWorkers()
	if err := a.waitConcurrentScan(ctx, job); err != nil {
		scanErr = err
		return
	}
	locked, root, kind, name, err := a.lockScanLibrary(ctx, lib)
	if err != nil {
		scanErr = err
		return
	}
	defer locked.Rollback()
	started := time.Now()
	a.scanner.mu.Lock()
	status := a.scanner.active[lib]
	status.Status = "scanning"
	a.scanner.active[lib] = status
	a.scanner.mu.Unlock()
	a.changeActivity(job, func(entry *activityEntry) {
		entry.Name = name
		entry.State = "counting"
		entry.Current = "读取文件清单"
	})
	// The library status row stays locked until completion. The gateway overlays
	// the live in-memory status; the original scanner's status UPDATE waits here.
	defer func() {
		if ctx.Err() != nil {
			return
		}
		status, message := "idle", ""
		if scanErr != nil {
			status, message = "error", scanErr.Error()
		}
		var count int
		if err := a.db.DB.QueryRowContext(ctx, "SELECT count(*) FROM items WHERE lib=$1 AND kind IN ('Movie','Episode')", lib).Scan(&count); err != nil {
			if scanErr == nil {
				scanErr = err
			}
			return
		}
		if _, err := locked.ExecContext(ctx, "UPDATE libraries SET status=$1,error=$2,count=$3,duration=$4,scanned=$5 WHERE id=$6", status, message, count, time.Since(started).Seconds(), time.Now().Unix(), lib); err != nil {
			if scanErr == nil {
				scanErr = err
			}
			return
		}
		if err := locked.Commit(); err != nil && scanErr == nil {
			scanErr = err
		}
	}()
	previous, err := a.previousSnapshot(lib)
	if err != nil {
		scanErr = err
		return
	}
	roots := a.libraryPaths(lib, root)
	inventory, err := a.inventoryForScan(ctx, job, roots, scopes)
	if err != nil {
		scanErr = err
		return
	}
	if len(scopes) > 0 {
		for path, stamp := range previous {
			if !withinScanScopes(path, scopes) {
				inventory.Snapshot[path] = stamp
			}
		}
	}
	cache := newScanCache(workerCtx, inventory)
	dirty := changedSidecars(previous, inventory.Snapshot)
	concurrency := status.Concurrency
	if concurrency > len(inventory.Files) {
		concurrency = len(inventory.Files)
	}
	a.changeActivity(job, func(entry *activityEntry) {
		entry.Total = len(inventory.Files)
		entry.State = "running"
		entry.Current = fmt.Sprintf("单库扫描并发 %d", status.Concurrency)
	})
	jobs := make(chan scanWork, max(1, concurrency*2))
	results := make(chan scanResult, max(1, concurrency*2))
	dispatchDone := make(chan error, 1)
	var workers sync.WaitGroup
	for worker := 0; worker < concurrency; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for work := range jobs {
				if err := a.waitConcurrentScan(workerCtx, job); err != nil {
					return
				}
				result := parseScanFile(cache, lib, kind, work)
				select {
				case <-workerCtx.Done():
					return
				case results <- result:
				}
			}
		}()
	}
	go func() {
		dispatchDone <- a.dispatchScanFiles(workerCtx, jobs, inventory, previous, dirty, incremental || len(scopes) > 0)
	}()
	go func() { workers.Wait(); close(results) }()
	seenParents := map[string]bool{}
	batch := make([]parsedScanItem, 0, 256)
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if err := a.writeScanBatch(ctx, cache, batch, idGeneration(job)); err != nil {
			return err
		}
		batch = batch[:0]
		return nil
	}
	done, changed := 0, 0
	for result := range results {
		if scanErr != nil {
			continue
		}
		if result.Err != nil {
			scanErr = result.Err
			stopWorkers()
			continue
		}
		for _, parsed := range result.Items {
			if parsed.Item.Kind == "Series" || parsed.Item.Kind == "Season" {
				if seenParents[parsed.Item.ID] {
					continue
				}
				seenParents[parsed.Item.ID] = true
			}
			batch = append(batch, parsed)
		}
		if len(result.Items) > 0 {
			changed++
		}
		done++
		if len(batch) >= 200 {
			if err := flush(); err != nil {
				scanErr = err
				stopWorkers()
				continue
			}
		}
		a.changeActivity(job, func(entry *activityEntry) {
			entry.Done = done
			if entry.Total > 0 {
				entry.Progress = float64(done) * 100 / float64(entry.Total)
			}
			entry.Current = fmt.Sprintf("单库并发 %d · %d/%d · 更新 %d · %s", status.Concurrency, done, len(inventory.Files), changed, filepath.Base(result.Path))
		})
	}
	dispatchErr := <-dispatchDone
	if scanErr != nil {
		return
	}
	if dispatchErr != nil {
		scanErr = dispatchErr
		return
	}
	if ctx.Err() != nil {
		scanErr = ctx.Err()
		return
	}
	if scanErr = flush(); scanErr != nil {
		return
	}
	a.changeActivity(job, func(entry *activityEntry) { entry.State = "cleaning"; entry.Current = "提交文件快照" })
	if err := a.waitConcurrentScan(ctx, job); err != nil {
		scanErr = err
		return
	}
	// Recheck mount availability before reconciling deletions.
	for _, path := range roots {
		if info, err := os.Stat(path); err != nil || !info.IsDir() {
			scanErr = fmt.Errorf("媒体目录不可用，保留原索引与快照")
			return
		}
	}
	scanErr = a.commitSnapshot(lib, inventory.Snapshot, allowEmpty, scopes)
	if scanErr == nil {
		a.changeActivity(job, func(entry *activityEntry) {
			entry.Done = len(inventory.Files)
			entry.Progress = 100
			entry.Current = fmt.Sprintf("扫描完成 · 并发 %d · 更新 %d", status.Concurrency, changed)
		})
	}
}

func idGeneration(job string) string { return "parallel-" + job }
