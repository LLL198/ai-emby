package main

import (
	"context"
	"crypto/sha256"
	"sync"
	"time"
)

const releaseTimeFormat = "2006.01.02-150405"

func compareReleaseVersions(current, latest string) (known, newer bool) {
	currentTime, err := time.Parse(releaseTimeFormat, current)
	if err != nil || currentTime.Format(releaseTimeFormat) != current {
		return false, false
	}
	latestTime, err := time.Parse(releaseTimeFormat, latest)
	if err != nil || latestTime.Format(releaseTimeFormat) != latest {
		return false, false
	}
	return true, latestTime.After(currentTime)
}

type releaseChecker struct {
	once        sync.Once
	gate        chan struct{}
	proxy       [32]byte
	until       time.Time
	release     releaseInfo
	manifest    updateManifest
	manifestURL string
	err         error
}

var updates releaseChecker

func (checker *releaseChecker) latest(ctx context.Context, proxy string) (releaseInfo, updateManifest, string, error) {
	checker.once.Do(func() { checker.gate = make(chan struct{}, 1) })
	select {
	case checker.gate <- struct{}{}:
	case <-ctx.Done():
		return releaseInfo{}, updateManifest{}, "", ctx.Err()
	}
	defer func() { <-checker.gate }()
	if err := ctx.Err(); err != nil {
		return releaseInfo{}, updateManifest{}, "", err
	}
	key := sha256.Sum256([]byte(proxy))
	if key == checker.proxy && time.Now().Before(checker.until) {
		return checker.release, checker.manifest, checker.manifestURL, checker.err
	}
	fetchContext, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	release, manifest, endpoint, err := checker.fetch(fetchContext, proxy)
	if contextErr := fetchContext.Err(); contextErr != nil {
		return release, manifest, endpoint, contextErr
	}
	checker.proxy, checker.release, checker.manifest, checker.manifestURL, checker.err = key, release, manifest, endpoint, err
	duration := time.Minute
	if err != nil {
		duration = 5 * time.Second
	}
	checker.until = time.Now().Add(duration)
	return release, manifest, endpoint, err
}
