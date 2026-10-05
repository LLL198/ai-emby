package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
)

// Limit media workers separately from the shared TMDB request rate.
const maxScraperConcurrency = 32

func scraperConcurrency(value int) int {
	if value < 1 {
		return 1
	}
	if value > maxScraperConcurrency {
		return maxScraperConcurrency
	}
	return value
}

// The caller owns the task lifetime and resets running/cancel only after this
// method joins every worker. Providers and per-object targets retain their
// existing cancellation, pause, write locking and refresh queue behavior.
func (a *App) runScraperConcurrent(ctx context.Context, plan *scraperPlan, activityID string, provider Scraper, concurrency int) error {
	if concurrency > len(plan.Objects) {
		concurrency = len(plan.Objects)
	}
	jobs := make(chan scraperObject)
	var workers sync.WaitGroup
	var progress sync.Mutex
	done, failures := 0, 0
	update := func(object scraperObject, completed, failed bool) {
		progress.Lock()
		defer progress.Unlock()
		if completed {
			done++
		}
		if failed {
			failures++
		}
		safeName := a.scraperSafeError(errors.New(object.Name)).Error()
		a.changeActivity(activityID, func(entry *activityEntry) {
			entry.State = "running"
			entry.Total, entry.Done = len(plan.Objects), done
			entry.Name = safeName
			entry.Current = fmt.Sprintf("刮削并发 %d · 已完成 %d/%d · 失败 %d", concurrency, done, len(plan.Objects), failures)
			if len(plan.Objects) > 0 {
				entry.Progress = float64(done) * 100 / float64(len(plan.Objects))
			}
		})
	}
	for worker := 0; worker < concurrency; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for object := range jobs {
				if a.waitScraper(ctx, activityID) != nil || ctx.Err() != nil {
					return
				}
				update(object, false, false)
				if object.Disabled {
					update(object, true, false)
					continue
				}
				itemActivity := a.newActivity("scraper", object.ID, a.scraperSafeError(errors.New(object.Name)).Error())
				err := a.scrapeObject(ctx, plan.Config, object, provider, itemActivity)
				a.finishActivity(itemActivity, a.scraperSafeError(err))
				root, _ := a.scraperRoot(object.Item.Lib, object.Item.Path)
				relative, _ := filepath.Rel(root, object.Item.Path)
				recognition := MediaRecognition{Kind: object.Kind, Title: object.Name}
				if object.Recognition != nil {
					recognition = *object.Recognition
				}
				phase, reason := "成功", "单媒体刮削结束"
				if err != nil {
					phase, reason = "失败", err.Error()
				}
				a.scraperPhase(phase, scraperScope{Library: object.Item.Lib, Root: root, Relative: relative}, recognition, plan.Config.Scraper, reason)
				update(object, true, err != nil)
			}
		}()
	}
dispatch:
	for _, object := range plan.Objects {
		select {
		case <-ctx.Done():
			break dispatch
		case jobs <- object:
		}
	}
	close(jobs)
	workers.Wait()
	if err := ctx.Err(); err != nil {
		a.finishActivity(activityID, errors.New("刮削已取消"))
		return err
	}
	var err error
	if failures > 0 {
		err = fmt.Errorf("%d 个媒体失败，详见单片日志", failures)
	}
	// An empty plan completes too, without division by zero or starting workers.
	a.changeActivity(activityID, func(entry *activityEntry) {
		entry.Total, entry.Done = len(plan.Objects), done
		entry.Progress = 100
		entry.Current = fmt.Sprintf("完成，失败 %d", failures)
	})
	a.finishActivity(activityID, err)
	return err
}
