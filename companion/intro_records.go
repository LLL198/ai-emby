package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const introRecordLimit = 16384

func introSourcePriority(source string) int {
	switch source {
	case "manual":
		return 3
	case "introdb", "imported":
		return 2
	case "behavior":
		return 1
	}
	return 0
}

func mergeIntroRecord(existing, incoming IntroRecord) IntroRecord {
	previous, next := introSourcePriority(existing.Source), introSourcePriority(incoming.Source)
	if next == 0 {
		return existing
	}
	if previous == 0 || next > previous || (next == previous && !incoming.UpdatedAt.Before(existing.UpdatedAt)) {
		return incoming
	}
	return existing
}

func introDataRoot() string {
	root := strings.TrimSpace(os.Getenv("MEDIA_INFO_ROOT"))
	if root == "" {
		root = "/app/data"
	}
	return root
}

func introPathWithin(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != "." && filepath.IsLocal(relative)
}

func validateIntroDirectory(directory string) (string, error) {
	root, err := filepath.Abs(introDataRoot())
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(directory) == "" || !filepath.IsAbs(directory) {
		return "", errors.New("片头片尾目录必须使用绝对路径")
	}
	directory = filepath.Clean(strings.TrimSpace(directory))
	media := filepath.Join(root, "media-info")
	if !introPathWithin(root, directory) || directory == media || introPathWithin(media, directory) {
		return "", errors.New("片头片尾目录必须位于数据根目录且不能使用媒体信息目录")
	}
	if err = os.MkdirAll(root, 0o700); err != nil {
		return "", err
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	ancestor := directory
	for {
		_, err = os.Lstat(ancestor)
		if err == nil {
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return "", errors.New("片头片尾目录无效")
		}
		ancestor = parent
	}
	resolvedAncestor, err := filepath.EvalSymlinks(ancestor)
	if err != nil || (resolvedAncestor != resolvedRoot && !introPathWithin(resolvedRoot, resolvedAncestor)) {
		return "", errors.New("片头片尾目录不能指向数据根目录之外")
	}
	if err = os.MkdirAll(directory, 0o700); err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(directory)
	if err != nil || !introPathWithin(resolvedRoot, resolved) {
		return "", errors.New("片头片尾目录不能指向数据根目录之外")
	}
	resolvedMedia := filepath.Join(resolvedRoot, "media-info")
	if resolved == resolvedMedia || introPathWithin(resolvedMedia, resolved) {
		return "", errors.New("片头片尾目录不能使用媒体信息目录")
	}
	probe, err := os.CreateTemp(resolved, ".write-test-*")
	if err != nil {
		return "", err
	}
	err = probe.Close()
	removeErr := os.Remove(probe.Name())
	if err != nil {
		return "", err
	}
	if removeErr != nil {
		return "", removeErr
	}
	return resolved, nil
}

func introRecordPath(directory string, record IntroRecord) string {
	return filepath.Join(directory, digest(record.SeriesID), fmt.Sprintf("season-%04d.json", record.Season))
}

func introImportPath(directory string, record IntroRecord) string {
	return introRecordPath(filepath.Join(directory, "imported", "IntroDb"), record)
}

func validIntroRecord(record IntroRecord) bool {
	return record.Version == 1 && record.SeriesID != "" && record.Season >= 0 &&
		introSourcePriority(record.Source) > 0 && record.IntroStartTicks >= 0 &&
		record.IntroEndTicks >= record.IntroStartTicks && record.CreditsStartTicks >= 0 &&
		record.Confidence >= 0 && record.Confidence <= 1
}

func readIntroRecord(path string) (IntroRecord, error) {
	var record IntroRecord
	info, err := os.Lstat(path)
	if err != nil {
		return record, err
	}
	if !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return record, errors.New("片头片尾文件不是有效的小型 JSON 文件")
	}
	file, err := os.Open(path)
	if err != nil {
		return record, err
	}
	defer file.Close()
	err = json.NewDecoder(io.LimitReader(file, (1<<20)+1)).Decode(&record)
	if err == nil && !validIntroRecord(record) {
		err = errors.New("片头片尾文件格式无效")
	}
	return record, err
}

func writeIntroRecord(directory string, record IntroRecord) error {
	if !validIntroRecord(record) {
		return errors.New("片头片尾记录无效")
	}
	root, err := validateIntroDirectory(directory)
	if err != nil {
		return err
	}
	path := introRecordPath(root, record)
	parent := filepath.Dir(path)
	if err = os.MkdirAll(parent, 0o700); err != nil {
		return err
	}
	resolved, err := filepath.EvalSymlinks(parent)
	if err != nil || resolved != parent {
		return errors.New("片头片尾文件目录无效")
	}
	if previous, readErr := readIntroRecord(path); readErr == nil && previous.SeriesID == record.SeriesID && previous.Season == record.Season {
		record = mergeIntroRecord(previous, record)
	} else if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return readErr
	}
	return writeIntroJSON(path, record)
}

func writeIntroJSON(path string, record IntroRecord) error {
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".intro-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	_, err = file.Write(data)
	if err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

func clearIntroRecordFiles(directory string) error {
	root, err := validateIntroDirectory(directory)
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() || len(entry.Name()) != 64 || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		directory := filepath.Join(root, entry.Name())
		files, err := os.ReadDir(directory)
		if err != nil {
			return err
		}
		for _, file := range files {
			if !file.Type().IsRegular() || !strings.HasPrefix(file.Name(), "season-") || !strings.HasSuffix(file.Name(), ".json") {
				continue
			}
			path := filepath.Join(directory, file.Name())
			record, err := readIntroRecord(path)
			if err != nil || introRecordPath(root, record) != path {
				continue
			}
			if err = os.Remove(path); err != nil {
				return err
			}
		}
		_ = os.Remove(directory)
	}
	return nil
}

func walkIntroRecords(directory string, visit func(string, IntroRecord) error) error {
	count := 0
	return filepath.WalkDir(directory, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 || entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			return nil
		}
		count++
		if count > introRecordLimit {
			return fs.SkipAll
		}
		record, err := readIntroRecord(path)
		if err != nil {
			return visit(path, IntroRecord{})
		}
		return visit(path, record)
	})
}

func migrateIntroDirectory(previous, directory string) error {
	if previous == "" || previous == directory {
		return nil
	}
	oldRoot, err := validateIntroDirectory(previous)
	if err != nil {
		return err
	}
	root, err := validateIntroDirectory(directory)
	if err != nil || root == oldRoot {
		return err
	}
	return walkIntroRecords(oldRoot, func(path string, record IntroRecord) error {
		if introPathWithin(root, path) || !validIntroRecord(record) {
			return nil
		}
		if path == introRecordPath(oldRoot, record) {
			return writeIntroRecord(root, record)
		}
		if path != introImportPath(oldRoot, record) || (record.Source != "introdb" && record.Source != "imported") {
			return nil
		}
		target := introImportPath(root, record)
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		resolved, err := filepath.EvalSymlinks(filepath.Dir(target))
		if err != nil || !introPathWithin(root, resolved) {
			return errors.New("导入记录目录无效")
		}
		if _, err = os.Lstat(target); err == nil {
			return nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return writeIntroJSON(target, record)
	})
}

func (a *App) seedIntroRecordFiles(directory string, generation uint64) {
	cursor, season := "", -1
	for {
		if a.intro.ctx.Err() != nil {
			return
		}
		rows, err := a.db.Query("SELECT series_id,season,intro_start_ticks,intro_end_ticks,credits_start_ticks,source,confidence,updated_at FROM intro_markers WHERE series_id>? OR (series_id=? AND season>?) ORDER BY series_id,season LIMIT 100", cursor, cursor, season)
		if err != nil {
			a.introLog("", "导出片头片尾记录失败", err)
			return
		}
		batch := []IntroRecord{}
		for rows.Next() {
			record := IntroRecord{Version: 1}
			var updated int64
			if err = rows.Scan(&record.SeriesID, &record.Season, &record.IntroStartTicks, &record.IntroEndTicks, &record.CreditsStartTicks, &record.Source, &record.Confidence, &updated); err != nil {
				break
			}
			record.UpdatedAt = time.Unix(updated, 0)
			batch = append(batch, record)
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			a.introLog("", "导出片头片尾记录失败", err)
			return
		}
		if len(batch) == 0 {
			return
		}
		for _, record := range batch {
			if a.intro.ctx.Err() != nil {
				return
			}
			a.intro.persistMu.Lock()
			a.intro.mu.Lock()
			current := generation == a.intro.generation && directory == a.intro.config.Directory
			a.intro.mu.Unlock()
			if !current {
				a.intro.persistMu.Unlock()
				return
			}
			path := introRecordPath(directory, record)
			_, err = os.Lstat(path)
			if errors.Is(err, os.ErrNotExist) {
				err = writeIntroRecord(directory, record)
			}
			a.intro.persistMu.Unlock()
			if err != nil {
				a.introLog("", "导出片头片尾文件失败", err)
			}
			cursor, season = record.SeriesID, record.Season
		}
	}
}

func (a *App) queueIntroRecord(record IntroRecord, generation uint64) {
	select {
	case a.intro.recordQueue <- introRecordWrite{record: record, generation: generation}:
	default:
		a.introLog(record.ItemID, "片头片尾保存队列已满", errors.New("稍后可从数据库重新导出"))
	}
}

func (a *App) introRecordWorker() {
	pending := map[string]introRecordWrite{}
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	store := func(job introRecordWrite) {
		key := fmt.Sprintf("%s:%d", job.record.SeriesID, job.record.Season)
		if previous, exists := pending[key]; exists && previous.generation == job.generation {
			job.record = mergeIntroRecord(previous.record, job.record)
		}
		if len(pending) >= introRecordLimit {
			if _, exists := pending[key]; !exists {
				a.introLog(job.record.ItemID, "片头片尾保存队列已满", errors.New("稍后可从数据库重新导出"))
				return
			}
		}
		pending[key] = job
	}
	flush := func() {
		for key, job := range pending {
			delete(pending, key)
			a.intro.persistMu.Lock()
			a.intro.mu.Lock()
			directory := a.intro.config.Directory
			current := job.generation == a.intro.generation
			a.intro.mu.Unlock()
			var err error
			if current {
				err = writeIntroRecord(directory, job.record)
			}
			a.intro.persistMu.Unlock()
			if err != nil {
				a.introLog(job.record.ItemID, "保存片头片尾文件失败", err)
			}
		}
	}
	for {
		select {
		case job := <-a.intro.recordQueue:
			store(job)
		case <-ticker.C:
			flush()
		case <-a.intro.ctx.Done():
			<-a.intro.learningDone
			for {
				select {
				case job := <-a.intro.recordQueue:
					store(job)
				default:
					flush()
					return
				}
			}
		}
	}
}

func (a *App) loadIntroRecords(directory string, generation uint64) {
	root, err := validateIntroDirectory(directory)
	if err == nil {
		err = walkIntroRecords(root, func(path string, record IntroRecord) error {
			if a.intro.ctx.Err() != nil {
				return fs.SkipAll
			}
			if !validIntroRecord(record) {
				a.introLog("", "片头片尾文件格式无效", errors.New(filepath.Base(path)))
				return nil
			}
			canonical := path == introRecordPath(root, record)
			imported := path == introImportPath(root, record) && (record.Source == "introdb" || record.Source == "imported")
			if canonical || imported {
				if err := a.applyIntroRecord(record, generation); err != nil {
					a.introLog(record.ItemID, "导入片头片尾文件失败", err)
				}
			}
			return nil
		})
	}
	if err != nil {
		a.introLog("", "扫描片头片尾目录失败", err)
	}
}

func (a *App) applyIntroRecord(record IntroRecord, generation uint64) error {
	if !validIntroRecord(record) {
		return errors.New("片头片尾记录无效")
	}
	a.intro.persistMu.Lock()
	defer a.intro.persistMu.Unlock()
	a.intro.mu.Lock()
	current := generation == a.intro.generation
	a.intro.mu.Unlock()
	if !current {
		return nil
	}
	var parent string
	err := a.db.QueryRow("SELECT id FROM items WHERE kind='Season' AND parent=? AND season=? LIMIT 1", record.SeriesID, record.Season).Scan(&parent)
	if errors.Is(err, sql.ErrNoRows) {
		parent = record.SeriesID
	} else if err != nil {
		return err
	}
	previous := IntroRecord{Version: 1, SeriesID: record.SeriesID, Season: record.Season}
	var updated int64
	err = a.db.QueryRow("SELECT intro_start_ticks,intro_end_ticks,credits_start_ticks,source,confidence,updated_at FROM intro_markers WHERE series_id=? AND season=?", record.SeriesID, record.Season).Scan(&previous.IntroStartTicks, &previous.IntroEndTicks, &previous.CreditsStartTicks, &previous.Source, &previous.Confidence, &updated)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	previous.UpdatedAt = time.Unix(updated, 0)
	record = mergeIntroRecord(previous, record)
	if record.IntroEndTicks == 0 && record.CreditsStartTicks == 0 {
		return nil
	}
	_, err = a.db.Exec("INSERT INTO intro_markers(series_id,season,parent_id,intro_start_ticks,intro_end_ticks,credits_start_ticks,source,confidence,updated_at) VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(series_id,season) DO UPDATE SET parent_id=excluded.parent_id,intro_start_ticks=excluded.intro_start_ticks,intro_end_ticks=excluded.intro_end_ticks,credits_start_ticks=excluded.credits_start_ticks,source=excluded.source,confidence=excluded.confidence,updated_at=excluded.updated_at", record.SeriesID, record.Season, parent, record.IntroStartTicks, record.IntroEndTicks, record.CreditsStartTicks, record.Source, record.Confidence, record.UpdatedAt.Unix())
	if err != nil {
		return err
	}
	a.intro.mu.Lock()
	a.storeIntroMarker(parent, introMarker{SeriesID: record.SeriesID, Season: record.Season, IntroStartTicks: record.IntroStartTicks, IntroEndTicks: record.IntroEndTicks, CreditsStartTicks: record.CreditsStartTicks, Source: record.Source})
	a.intro.mu.Unlock()
	return nil
}
