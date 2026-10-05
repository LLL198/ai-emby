package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const namingMaxItems = 100000

var errNamingDirectoryTooLarge = errors.New("当前目录超过 500000 项，请选择更小的目录")

type namingScope struct {
	Files       []string
	Entries     map[string][]fs.DirEntry
	Errors      map[string]string
	Directories int
	Visited     int
}

func namingCollect(ctx context.Context, root *os.Root, base string, selected map[string]bool, request namingRequest, progress func(int, string)) (namingScope, error) {
	scope := namingScope{Entries: map[string][]fs.DirEntry{}, Errors: map[string]string{}}
	seen := map[string]bool{}
	walked := map[string]bool{}
	add := func(path string) error {
		if !seen[path] {
			seen[path] = true
			scope.Files = append(scope.Files, path)
			if len(scope.Files) > namingMaxItems {
				return fmt.Errorf("扫描超过 %d 个媒体项目，请选择更小的文件夹；本次尚未改名", namingMaxItems)
			}
		}
		return nil
	}
	var walk func(string, bool, int) error
	walk = func(path string, include bool, depth int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		info, err := fileCheck(root, path)
		if err == nil {
			allowed, descend := request.scopeAccess(path, info.IsDir())
			if !allowed && (!info.IsDir() || !descend) {
				return nil
			}
			include = include && allowed
		}
		if err != nil {
			scope.Errors[path] = "源文件或目录不可访问，已跳过"
			return add(path)
		}
		if !info.IsDir() {
			if info.Mode().IsRegular() && featureMediaExtension(filepath.Base(path)) && request.Kind != "directory" {
				return add(path)
			}
			return nil
		}
		if include && request.Mode != "sequence" && (request.Folders || request.Kind == "directory") {
			if err := add(path); err != nil {
				return err
			}
		}
		if include && !request.Recursive {
			return nil
		}
		if depth > 128 {
			scope.Errors[path] = "目录层级超过 128 层，已跳过这个分支"
			return add(path)
		}
		if walked[path] {
			return nil
		}
		walked[path] = true
		entries, err := namingEntries(root, path)
		if err != nil {
			if errors.Is(err, errNamingDirectoryTooLarge) {
				return err
			}
			scope.Errors[path] = "无法读取这个目录，已跳过其中的文件"
			return add(path)
		}
		scope.Entries[path] = entries
		scope.Directories++
		progress(len(scope.Files), path)
		for _, entry := range entries {
			scope.Visited++
			if scope.Visited > 500000 {
				return errors.New("扫描超过 500000 个目录项，请选择更小的文件夹；本次尚未改名")
			}
			if entry.Type()&os.ModeSymlink != 0 {
				continue
			}
			child := filepath.Join(path, entry.Name())
			if path == base && len(selected) > 0 && !selected[child] {
				continue
			}
			if request.Mode == "sequence" && entry.IsDir() {
				continue
			}
			if !entry.IsDir() && !featureMediaExtension(entry.Name()) {
				continue
			}
			if err := walk(child, true, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	if request.UseScraperScopes {
		for _, library := range request.ScopeRoots {
			path, err := filepath.Rel(fileRoot(), library.Root)
			if err != nil {
				return scope, err
			}
			if err := walk(path, false, 0); err != nil {
				return scope, err
			}
		}
	} else {
		if err := walk(base, false, 0); err != nil {
			return scope, err
		}
	}
	sort.SliceStable(scope.Files, func(i, j int) bool { return namingNatural(scope.Files[i], scope.Files[j]) })
	return scope, nil
}

func namingSidecarIndex(entries []fs.DirEntry) map[string][]fs.DirEntry {
	index := map[string][]fs.DirEntry{}
	for _, entry := range entries {
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		name := entry.Name()
		switch strings.ToLower(filepath.Ext(name)) {
		case ".nfo", ".jpg", ".jpeg", ".png", ".webp", ".srt", ".ass", ".ssa", ".sub", ".idx", ".vtt", ".sup":
		default:
			continue
		}
		for position, character := range name {
			if position > 0 && (character == '.' || character == '-') {
				index[name[:position]] = append(index[name[:position]], entry)
			}
		}
	}
	return index
}

func namingRelatedEntries(index map[string][]fs.DirEntry, path string) []fs.DirEntry {
	name := filepath.Base(path)
	stem := strings.TrimSuffix(name, filepath.Ext(name))
	short, _ := namingSplit(name)
	seen := map[string]bool{}
	entries := []fs.DirEntry{}
	for _, key := range []string{stem, short} {
		for _, entry := range index[key] {
			if !seen[entry.Name()] {
				seen[entry.Name()] = true
				entries = append(entries, entry)
			}
		}
	}
	return entries
}

func namingSortMoves(moves []namingMove) {
	sort.SliceStable(moves, func(i, j int) bool {
		left, right := strings.Count(moves[i].Old, string(filepath.Separator)), strings.Count(moves[j].Old, string(filepath.Separator))
		if left != right {
			return left > right
		}
		if moves[i].Info.IsDir() != moves[j].Info.IsDir() {
			return !moves[i].Info.IsDir()
		}
		return namingNatural(moves[i].Old, moves[j].Old)
	})
}

type namingPathMap struct {
	files, directories map[string]string
}

func namingPathMapping(moves []namingMove) namingPathMap {
	mapping := namingPathMap{files: map[string]string{}, directories: map[string]string{}}
	for _, move := range moves {
		old, next := filepath.Join(fileRoot(), move.Old), filepath.Join(fileRoot(), move.New)
		if move.Info.IsDir() {
			mapping.directories[old] = next
		} else {
			mapping.files[old] = next
		}
	}
	return mapping
}

func (mapping namingPathMap) rebase(path string) string {
	mediaRoot := filepath.Clean(fileRoot())
	if path != mediaRoot && !strings.HasPrefix(path, mediaRoot+string(filepath.Separator)) {
		return path
	}
	original := path
	if next, ok := mapping.files[path]; ok {
		path = next
	}
	for dir := original; dir != mediaRoot && dir != "."; dir = filepath.Dir(dir) {
		if next, ok := mapping.directories[dir]; ok {
			path = next + strings.TrimPrefix(path, dir)
		}
		if filepath.Dir(dir) == dir {
			break
		}
	}
	return path
}

func namingCatalogMoves(moves []namingMove) []namingMove {
	directories := map[string]bool{}
	for _, move := range moves {
		if move.Info.IsDir() {
			directories[move.Old] = true
		}
	}
	roots := []namingMove{}
	for _, move := range moves {
		covered := false
		for parent := filepath.Dir(move.Old); parent != "."; parent = filepath.Dir(parent) {
			if directories[parent] {
				covered = true
				break
			}
			if filepath.Dir(parent) == parent {
				break
			}
		}
		if !covered {
			roots = append(roots, move)
		}
	}
	return roots
}

type namingProgress struct {
	Phase   string    `json:"phase"`
	Done    int       `json:"done"`
	Total   int       `json:"total"`
	Current string    `json:"current"`
	Updated time.Time `json:"updated"`
	Owner   string    `json:"-"`
}

func (a *App) namingProgressUpdate(key, owner, phase string, done, total int, current string) {
	a.naming.progressMu.Lock()
	defer a.naming.progressMu.Unlock()
	if a.naming.progress == nil {
		a.naming.progress = map[string]namingProgress{}
	}
	for id, value := range a.naming.progress {
		if time.Since(value.Updated) > time.Hour {
			delete(a.naming.progress, id)
		}
	}
	a.naming.progress[owner+":"+key] = namingProgress{Phase: phase, Done: done, Total: total, Current: current, Updated: time.Now(), Owner: owner}
}

func (a *App) namingProgressAPI(w http.ResponseWriter, r *http.Request, user User) {
	if !featureMethod(w, r, http.MethodGet) {
		return
	}
	key := r.URL.Query().Get("ID")
	if key == "" || len(key) > 80 {
		fail(w, 400, "无效命名进度 ID")
		return
	}
	a.naming.progressMu.Lock()
	progress, ok := a.naming.progress[user.ID+":"+key]
	a.naming.progressMu.Unlock()
	if !ok {
		respond(w, namingProgress{Phase: "等待扫描"})
		return
	}
	respond(w, progress)
}
