package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const subtitleOwner = "ai-emby-subtitle-v1"

var subtitleOwnedName = regexp.MustCompile(`^auto-[a-f0-9]{16}\.(srt|ass|ssa|vtt)$`)
var subtitleCacheDirectory = regexp.MustCompile(`^[a-f0-9]{64}$`)

func subtitleKey(item Item) string {
	if item.Kind == "Episode" {
		return fmt.Sprintf("%s/series-season:%s/S%02dE%02d", item.ID, item.Parent, item.Season, item.Episode)
	}
	return item.ID
}

func subtitleFingerprint(item Item) string {
	return digest(subtitleKey(item) + "|" + item.Path + "|" + item.URL + fmt.Sprint(item.Mtime, item.Size))
}

func subtitleDirectory(item Item) string { return digest(subtitleKey(item)) }

func subtitleLogTitle(item Item) string {
	title := item.Name
	if item.Year > 0 {
		title += fmt.Sprintf(" (%d)", item.Year)
	}
	if item.Kind == "Episode" {
		title += fmt.Sprintf(" · S%02dE%02d · 第%d集", item.Season, item.Episode, item.Episode)
	}
	return title
}

func subtitleLanguageNames(language int) (name, tag string) {
	if language == 2 {
		return "简体中文", "zh-Hans"
	}
	if language == 1 {
		return "繁体中文", "zh-Hant"
	}
	return "", ""
}

func subtitleReadLimited(root *os.Root, path string, limit int64) ([]byte, error) {
	file, err := root.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > limit {
		return nil, errors.New("字幕文件无效或超限")
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, errors.New("字幕文件读取失败或超限")
	}
	return data, nil
}

func subtitleAtomicFile(root *os.Root, path string, data []byte) error {
	dir, err := root.OpenRoot(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	name := ".subtitle-" + id() + ".tmp"
	file, err := dir.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer dir.Remove(name)
	_, err = file.Write(data)
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return dir.Rename(name, filepath.Base(path))
}

func subtitleReadCache(item Item, config subtitleConfig) (subtitleRecord, bool) {
	root, err := os.OpenRoot(config.Directory)
	if err != nil {
		return subtitleRecord{}, false
	}
	defer root.Close()
	data, err := subtitleReadLimited(root, filepath.Join(subtitleDirectory(item), "record.json"), 16<<10)
	var record subtitleRecord
	if err != nil || json.Unmarshal(data, &record) != nil || record.ItemID != item.ID || record.Fingerprint != subtitleFingerprint(item) || !record.Verified || record.Source != "ASSRT" || record.Language != "zho" || record.Owner != subtitleOwner {
		return subtitleRecord{}, false
	}
	name, tag := subtitleLanguageNames(2)
	if record.LanguageTag == "zh-Hant" {
		name, tag = subtitleLanguageNames(1)
	}
	if record.LanguageTag != tag || record.LanguageName != name {
		return subtitleRecord{}, false
	}
	ext := filepath.Ext(record.Path)
	if !subtitleOwnedName.MatchString(filepath.Base(record.Path)) || strings.ToLower(filepath.Ext(record.FinalName)) != ext || record.ID != "auto-"+digest(record.Fingerprint)[:16] || record.Path != filepath.Join(subtitleDirectory(item), record.ID+ext) {
		return subtitleRecord{}, false
	}
	if _, err = subtitleReadLimited(root, record.Path, subtitleMaxBytes); err != nil {
		return subtitleRecord{}, false
	}
	return record, true
}

func subtitleRead(item Item, config subtitleConfig) (subtitleRecord, bool) {
	if record, ok := subtitleReadCache(item, config); ok {
		return record, true
	}
	return subtitleReadMedia(item)
}

func subtitleWriteNamed(item Item, config subtitleConfig, data []byte, ext, finalName string, language, score int) error {
	name, tag := subtitleLanguageNames(language)
	if tag == "" {
		return errors.New("字幕最终语言未知")
	}
	switch ext {
	case ".srt", ".ass", ".ssa", ".vtt":
	default:
		return errors.New("字幕最终文件类型不支持")
	}
	if finalName != "" && strings.ToLower(filepath.Ext(finalName)) != ext {
		return errors.New("字幕最终文件名与类型不一致")
	}
	if len(data) > subtitleMaxBytes || !subtitleTextValid(data, ext) {
		return errors.New("字幕内容无效")
	}
	if _, ok := subtitleReadCache(item, config); ok {
		return nil
	}
	if err := os.MkdirAll(config.Directory, 0o700); err != nil {
		return err
	}
	root, err := os.OpenRoot(config.Directory)
	if err != nil {
		return err
	}
	defer root.Close()
	dir := subtitleDirectory(item)
	if err = root.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	fingerprint := subtitleFingerprint(item)
	key := "auto-" + digest(fingerprint)[:16]
	if finalName == "" {
		finalName = key + ext
	}
	record := subtitleRecord{Source: "ASSRT", Language: "zho", Path: filepath.Join(dir, key+ext), ItemID: item.ID, Fingerprint: fingerprint, ID: key, Verified: true, LanguageName: name, LanguageTag: tag, FinalName: finalName, Owner: subtitleOwner, Score: score, Time: time.Now().UTC()}
	if err = subtitleAtomicFile(root, record.Path, data); err != nil {
		return err
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		return err
	}
	return subtitleAtomicFile(root, filepath.Join(dir, "record.json"), encoded)
}

func subtitleRemove(item Item, config subtitleConfig) error {
	root, err := os.OpenRoot(config.Directory)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer root.Close()
	return subtitleRemoveDirectory(root, subtitleDirectory(item))
}

func subtitleRemoveDirectory(root *os.Root, path string) error {
	dir, err := root.OpenRoot(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer dir.Close()
	file, err := dir.Open(".")
	if err != nil {
		return err
	}
	entries, err := file.ReadDir(-1)
	file.Close()
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if name != "record.json" && name != "record.tmp" && !subtitleOwnedName.MatchString(name) && !(strings.HasPrefix(name, ".subtitle-") && strings.HasSuffix(name, ".tmp")) {
			continue
		}
		if entry.Type()&os.ModeSymlink != 0 || entry.IsDir() {
			continue
		}
		if err := dir.Remove(name); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	_ = root.Remove(path)
	return nil
}

func subtitleMediaHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func subtitleFileHash(path string) (string, error) {
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return "", err
	}
	defer root.Close()
	data, err := subtitleReadLimited(root, filepath.Base(path), subtitleMaxBytes)
	if err != nil {
		return "", err
	}
	return subtitleMediaHash(data), nil
}

func (a *App) subtitleClearCache(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		fail(w, 405, "DELETE required")
		return
	}
	a.subtitles.mu.Lock()
	for _, flight := range a.subtitles.flights {
		if flight.cancel != nil {
			flight.cancel()
		}
	}
	a.subtitles.matches = nil
	a.subtitles.retry = nil
	a.subtitles.mu.Unlock()
	a.subtitles.storage.Lock()
	defer a.subtitles.storage.Unlock()
	root, err := os.OpenRoot(a.subtitleSettings().Directory)
	if errors.Is(err, fs.ErrNotExist) {
		respond(w, M{"Message": "字幕缓存已清理"})
		return
	}
	if err != nil {
		fail(w, 500, "字幕缓存清理失败")
		return
	}
	defer root.Close()
	file, err := root.Open(".")
	if err != nil {
		fail(w, 500, "字幕缓存清理失败")
		return
	}
	entries, err := file.ReadDir(-1)
	file.Close()
	if err != nil {
		fail(w, 500, "字幕缓存清理失败")
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || !subtitleCacheDirectory.MatchString(entry.Name()) {
			continue
		}
		data, err := subtitleReadLimited(root, filepath.Join(entry.Name(), "record.json"), 16<<10)
		var record subtitleRecord
		if err != nil || json.Unmarshal(data, &record) != nil || record.Owner != subtitleOwner {
			continue
		}
		if subtitleRemoveDirectory(root, entry.Name()) != nil {
			fail(w, 500, "字幕缓存清理失败")
			return
		}
	}
	respond(w, M{"Message": "字幕缓存已清理"})
}
