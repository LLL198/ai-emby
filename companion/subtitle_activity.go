package main

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

var subtitleLogURL = regexp.MustCompile(`(?i)https?://\S+`)

type subtitleSkip string

func (reason subtitleSkip) Error() string { return string(reason) }

func (a *App) subtitleTaskKey(item Item) string {
	if series := a.resumeSeriesID(item); series != "" {
		return fmt.Sprintf("series:%s/S%02dE%02d", series, item.Season, item.Episode)
	}
	return subtitleKey(item)
}

func (a *App) subtitleTaskContext(ctx context.Context, item Item) (context.Context, string) {
	if activity, ok := ctx.Value(subtitleActivityKey{}).(string); ok {
		return ctx, activity
	}
	key := a.newActivity("subtitle", item.ID, subtitleLogURL.ReplaceAllString(subtitleLogTitle(item), "[链接已隐藏]"))
	a.changeActivity(key, func(entry *activityEntry) { entry.State = "running" })
	return context.WithValue(ctx, subtitleActivityKey{}, key), key
}

func (a *App) subtitleTaskLog(ctx context.Context, item Item, text string) {
	key, _ := ctx.Value(subtitleActivityKey{}).(string)
	created := key == ""
	if key == "" {
		ctx, key = a.subtitleTaskContext(ctx, item)
	}
	text = subtitleLogURL.ReplaceAllString(text, "[链接已隐藏]")
	if token := a.subtitleSettings().Token; token != "" {
		text = strings.ReplaceAll(text, token, "[隐藏]")
	}
	a.changeActivity(key, func(entry *activityEntry) { entry.Current = text })
	if created {
		a.finishActivity(key, nil)
	}
}
