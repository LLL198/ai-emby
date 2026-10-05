package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func validateTMDBDirectory(directory string, create bool) (string, error) {
	root, err := filepath.EvalSymlinks(mediaInfoRoot())
	if err != nil {
		return "", errors.New("数据目录不可用")
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return "", err
	}
	directory = filepath.Clean(strings.TrimSpace(directory))
	if !filepath.IsAbs(directory) {
		return "", errors.New("缓存目录必须使用绝对路径")
	}
	directory, err = resolveMissingMediaPath(directory)
	if err != nil || directory == root || !mediaPathWithin(root, directory) {
		return "", errors.New("缓存目录必须位于数据目录的子目录")
	}
	if create {
		if err = os.MkdirAll(directory, 0700); err != nil {
			return "", errors.New("缓存目录不可写")
		}
		directory, err = filepath.EvalSymlinks(directory)
		if err != nil || directory == root || !mediaPathWithin(root, directory) {
			return "", errors.New("缓存目录不能指向数据目录之外")
		}
	}
	return directory, nil
}

func publicTMDBSettings(settings tmdbConfig) tmdbConfig {
	settings.HasAPIKey = settings.APIKey != ""
	settings.APIKey = ""
	return settings
}

func (a *App) tmdbAdmin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodGet {
		respond(w, publicTMDBSettings(a.tmdbSettings()))
		return
	}
	if r.Method != http.MethodPut && r.Method != http.MethodDelete {
		fail(w, 405, "GET, PUT or DELETE required")
		return
	}
	if !a.tmdbLock(r.Context()) {
		return
	}
	defer a.tmdbUnlock()
	settings := a.tmdbSettings()
	if r.Method == http.MethodDelete {
		directory, err := validateTMDBDirectory(settings.Directory, false)
		if err != nil {
			fail(w, 400, err.Error())
			return
		}
		root, err := os.OpenRoot(directory)
		if os.IsNotExist(err) {
			respond(w, M{"Removed": 0})
			return
		}
		if err != nil {
			fail(w, 500, "读取缓存失败")
			return
		}
		defer root.Close()
		listing, err := root.Open(".")
		if err != nil {
			fail(w, 500, "读取缓存失败")
			return
		}
		defer listing.Close()
		removed := 0
		for {
			entries, listErr := listing.ReadDir(100)
			for _, entry := range entries {
				if !entry.Type().IsRegular() || !strings.HasPrefix(entry.Name(), "tmdb-") || !strings.HasSuffix(entry.Name(), ".json") {
					continue
				}
				file, err := root.Open(entry.Name())
				if err != nil {
					continue
				}
				data, err := io.ReadAll(io.LimitReader(file, (2<<20)+1))
				file.Close()
				var record tmdbRecord
				if err != nil || len(data) > 2<<20 || json.Unmarshal(data, &record) != nil || record.Key == "" || entry.Name() != "tmdb-"+digest(record.Key)+".json" {
					continue
				}
				if err = root.Remove(entry.Name()); err != nil && !os.IsNotExist(err) {
					fail(w, 500, "清理缓存失败")
					return
				}
				removed++
			}
			if listErr == io.EOF {
				break
			}
			if listErr != nil {
				fail(w, 500, "读取缓存失败")
				return
			}
		}
		activity := a.newActivity("tmdb", "", "清空 TMDB 缓存")
		a.finishActivity(activity, nil)
		respond(w, M{"Removed": removed})
		return
	}
	previousKey := settings.APIKey
	if !body(w, r, &settings) {
		return
	}
	if settings.APIKey == "" {
		settings.APIKey = previousKey
	}
	settings.HasAPIKey, settings.Valid, settings.Validation = false, false, ""
	settings.APIBase = strings.TrimRight(strings.TrimSpace(settings.APIBase), "/")
	address, err := url.Parse(settings.APIBase)
	if err != nil || address.Host == "" || (address.Scheme != "http" && address.Scheme != "https") || address.User != nil || address.RawQuery != "" || address.ForceQuery || address.Fragment != "" {
		fail(w, 400, "API 地址必须为 HTTP(S) 基础地址")
		return
	}
	if settings.RequestsPerSecond < 1 || settings.RequestsPerSecond > 100 {
		fail(w, 400, "请求频率必须为 1–100 次/秒")
		return
	}
	settings.Directory, err = validateTMDBDirectory(settings.Directory, true)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	check, err := os.CreateTemp(settings.Directory, ".tmdb-check-")
	if err != nil {
		fail(w, 400, "缓存目录不可写")
		return
	}
	check.Close()
	os.Remove(check.Name())
	activity := a.newActivity("tmdb", "", "TMDB 已关闭")
	if settings.Enabled {
		a.changeActivity(activity, func(entry *activityEntry) { entry.Name, entry.State = "TMDB API 验证", "running" })
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		var configuration M
		err = a.tmdbGet(ctx, "configuration", nil, &configuration, settings)
		cancel()
		if err != nil {
			a.finishActivity(activity, err)
			fail(w, 400, "TMDB API 验证失败："+err.Error())
			return
		}
	}
	data, err := json.Marshal(settings)
	if err == nil {
		_, err = a.db.Exec("INSERT INTO settings(k,v) VALUES('tmdb',?) ON CONFLICT(k) DO UPDATE SET v=excluded.v", string(data))
	}
	a.finishActivity(activity, err)
	if err != nil {
		fail(w, 500, "保存失败")
		return
	}
	settings.Valid = settings.Enabled
	if settings.Enabled {
		settings.Validation = "TMDB API 验证成功"
	} else {
		settings.Validation = "TMDB 已关闭"
	}
	respond(w, publicTMDBSettings(settings))
}
