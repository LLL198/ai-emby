package main

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type mediaSubtitleMarker struct {
	Owner       string
	ItemID      string
	Fingerprint string
	File        string
	Hash        string
	LanguageTag string
	Ext         string
}

func subtitleMediaLocation(item Item) (directory, stem string, err error) {
	path := item.Path
	if !filepath.IsAbs(path) || strings.ContainsRune(path, 0) || strings.Contains(path, "://") {
		return "", "", errors.New("媒体文件路径无效")
	}
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if part == "." || part == ".." {
			return "", "", errors.New("媒体文件路径无效")
		}
	}
	directory = filepath.Dir(path)
	real, err := filepath.EvalSymlinks(directory)
	if err != nil || real != directory {
		return "", "", errors.New("媒体目录不能包含符号链接")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return "", "", errors.New("媒体文件不是普通文件")
	}
	stem = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	return directory, stem, nil
}

func subtitleReadMedia(item Item) (subtitleRecord, bool) {
	dir, stem, err := subtitleMediaLocation(item)
	if err != nil {
		return subtitleRecord{}, false
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return subtitleRecord{}, false
	}
	defer root.Close()
	for _, language := range []int{2, 1} {
		languageName, tag := subtitleLanguageNames(language)
		markerName := "." + stem + "." + tag + ".ai-emby.json"
		data, err := subtitleReadLimited(root, markerName, 4<<10)
		var marker mediaSubtitleMarker
		if err != nil || json.Unmarshal(data, &marker) != nil || marker.Owner != subtitleOwner || marker.ItemID != item.ID || marker.Fingerprint != subtitleFingerprint(item) || marker.LanguageTag != tag {
			continue
		}
		switch marker.Ext {
		case ".srt", ".ass", ".ssa", ".vtt":
		default:
			continue
		}
		if marker.File != stem+"."+tag+marker.Ext {
			continue
		}
		content, err := subtitleReadLimited(root, marker.File, subtitleMaxBytes)
		if err != nil || subtitleMediaHash(content) != marker.Hash {
			continue
		}
		return subtitleRecord{Source: "ASSRT", Language: "zho", Path: filepath.Join(dir, marker.File), ItemID: item.ID, Fingerprint: marker.Fingerprint, ID: "auto-" + digest(marker.Fingerprint)[:16], Verified: true, LanguageName: languageName, LanguageTag: tag, FinalName: marker.File, Owner: subtitleOwner, Time: time.Now().UTC()}, true
	}
	return subtitleRecord{}, false
}

func subtitleWriteMedia(item Item, config subtitleConfig, record subtitleRecord) (subtitleRecord, error) {
	if !config.SaveBesideMedia {
		return record, nil
	}
	if !record.Verified || record.Owner != subtitleOwner || record.ItemID != item.ID || record.Fingerprint != subtitleFingerprint(item) {
		return subtitleRecord{}, errors.New("字幕记录无效")
	}
	dir, stem, err := subtitleMediaLocation(item)
	if err != nil {
		return subtitleRecord{}, err
	}
	cacheRoot, err := os.OpenRoot(config.Directory)
	if err != nil {
		return subtitleRecord{}, err
	}
	defer cacheRoot.Close()
	data, err := subtitleReadLimited(cacheRoot, record.Path, subtitleMaxBytes)
	if err != nil {
		return subtitleRecord{}, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return subtitleRecord{}, err
	}
	defer root.Close()
	ext := filepath.Ext(record.Path)
	filename := stem + "." + record.LanguageTag + ext
	markerName := "." + stem + "." + record.LanguageTag + ".ai-emby.json"
	var old mediaSubtitleMarker
	markerData, markerErr := subtitleReadLimited(root, markerName, 4<<10)
	markerValid := markerErr == nil && json.Unmarshal(markerData, &old) == nil && old.Owner == subtitleOwner && old.ItemID == item.ID && old.File == filename && old.LanguageTag == record.LanguageTag && old.Ext == ext
	if markerErr != nil && !errors.Is(markerErr, fs.ErrNotExist) {
		return subtitleRecord{}, markerErr
	}
	if markerErr == nil && !markerValid {
		return subtitleRecord{}, errors.New("已存在其他字幕记录，跳过覆盖")
	}
	_, statErr := root.Lstat(filename)
	if statErr == nil {
		existing, err := subtitleReadLimited(root, filename, subtitleMaxBytes)
		if err != nil || !markerValid || subtitleMediaHash(existing) != old.Hash {
			return subtitleRecord{}, errors.New("已存在非本程序生成的字幕，跳过覆盖")
		}
		if err = subtitleAtomicFile(root, filename, data); err != nil {
			return subtitleRecord{}, err
		}
	} else {
		if !errors.Is(statErr, fs.ErrNotExist) {
			return subtitleRecord{}, statErr
		}
		file, err := root.OpenFile(filename, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err != nil {
			return subtitleRecord{}, err
		}
		_, err = file.Write(data)
		if closeErr := file.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			_ = root.Remove(filename)
			return subtitleRecord{}, err
		}
	}
	marker := mediaSubtitleMarker{Owner: subtitleOwner, ItemID: item.ID, Fingerprint: record.Fingerprint, File: filename, Hash: subtitleMediaHash(data), LanguageTag: record.LanguageTag, Ext: ext}
	encoded, err := json.Marshal(marker)
	if err == nil {
		err = subtitleAtomicFile(root, markerName, encoded)
	}
	if err != nil {
		return subtitleRecord{}, err
	}
	record.Path, record.FinalName = filepath.Join(dir, filename), filename
	return record, nil
}
