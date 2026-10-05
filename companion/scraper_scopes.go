package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

type scraperFileScope struct {
	Library   string
	Root      string
	Relative  string
	Path      string
	Directory bool
}

type scraperScope struct {
	Library  string
	Root     string
	Relative string
	Enabled  bool
}

// scraperScopeEnabled applies the most specific matching scope. A relative
// path of "." covers the whole library; descendants are matched only across
// a path-component boundary.
func scraperScopeEnabled(scopes []scraperScope, library, root, relative string, fallback bool) bool {
	bestLength := -1
	enabled := fallback
	for _, scope := range scopes {
		if scope.Library != library || scope.Root != root {
			continue
		}

		matches := scope.Relative == "." || relative == scope.Relative || strings.HasPrefix(relative, scope.Relative+"/")
		length := len(scope.Relative)
		if scope.Relative == "." {
			length = 0
		}
		if matches && length > bestLength {
			bestLength = length
			enabled = scope.Enabled
		}
	}
	return enabled
}

// scraperScopePath validates a library-relative scope and returns an os.Root
// rooted at the selected directory. The selected path is resolved without
// following symlinks, using the same library-root checks as scraper writes.
func scraperScopePath(libraryRoot, relative string) (*os.Root, error) {
	if libraryRoot == "" || !filepath.IsAbs(libraryRoot) || filepath.Clean(libraryRoot) != libraryRoot ||
		relative == "" || filepath.Clean(relative) != relative || !filepath.IsLocal(relative) {
		return nil, errors.New("无效目录范围")
	}
	for _, part := range strings.Split(relative, "/") {
		if part == ".." {
			return nil, errors.New("拒绝上级目录")
		}
	}

	if relative == "." {
		return scraperLibraryRoot(libraryRoot)
	}

	root, relativePath, err := scraperOpen(libraryRoot, filepath.Join(libraryRoot, relative))
	if err != nil {
		return nil, err
	}
	defer root.Close()

	return root.OpenRoot(relativePath)
}

// scraperScopeValid requires a scope root to be one of the library's
// configured roots, then validates that its relative directory can be opened
// safely beneath that root.
func (a *App) scraperScopeValid(scope scraperScope) bool {
	var libraryPath string
	if err := a.db.QueryRow("SELECT path FROM libraries WHERE id=?", scope.Library).Scan(&libraryPath); err != nil {
		return false
	}
	for _, configuredRoot := range a.libraryPaths(scope.Library, libraryPath) {
		if configuredRoot != scope.Root {
			continue
		}
		scopeRoot, err := scraperScopePath(configuredRoot, scope.Relative)
		if err != nil {
			return false
		}
		_ = scopeRoot.Close()
		return true
	}
	return false
}

// Remove invalid or duplicate scopes in input order, keeping at most 4096 entries.
func (a *App) sanitizeScraperScopes(scopes []scraperScope) []scraperScope {
	if len(scopes) == 0 {
		return nil
	}

	seen := make(map[string]struct{})
	valid := make([]scraperScope, 0)
	for _, scope := range scopes {
		key := scope.Library + "\x00" + scope.Root + "\x00" + scope.Relative
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		if !a.scraperScopeValid(scope) {
			continue
		}
		valid = append(valid, scope)
		if len(valid) >= 4096 {
			return valid
		}
	}
	return valid
}

// sanitizeMonitorScopes retains only whole-root monitor entries before using
// the common de-duplication and filesystem validation path.
func (a *App) sanitizeMonitorScopes(scopes []scraperScope) []scraperScope {
	roots := make([]scraperScope, 0)
	for _, scope := range scopes {
		if scope.Relative == "." {
			roots = append(roots, scope)
		}
	}
	return a.sanitizeScraperScopes(roots)
}

// scraperTree lists child directories for a validated scope, or returns
// matching media-library directories when a search term is supplied.
func (a *App) scraperTree(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		fail(w, http.StatusMethodNotAllowed, "GET required")
		return
	}

	query := r.URL.Query()
	search := strings.TrimSpace(query.Get("search"))
	if search == "" {
		libraryID := query.Get("library")
		if libraryID == "" {
			respond(w, M{"Libraries": a.libraries()})
			return
		}

		scope := scraperScope{
			Library:  libraryID,
			Root:     query.Get("root"),
			Relative: query.Get("relative"),
		}
		a.scraper.mu.Lock()
		defer a.scraper.mu.Unlock()
		valid := a.scraperScopeValid(scope)
		if !valid {
			fail(w, http.StatusBadRequest, "目录已移除或不安全")
			return
		}

		root, err := scraperScopePath(scope.Root, scope.Relative)
		if err != nil {
			fail(w, http.StatusBadRequest, "目录不可访问")
			return
		}
		defer root.Close()

		directory, err := root.OpenFile(".", os.O_RDONLY, 0)
		if err != nil {
			fail(w, http.StatusBadRequest, "目录不可访问")
			return
		}
		defer directory.Close()

		entries, err := directory.ReadDir(-1)
		if err != nil {
			fail(w, http.StatusBadRequest, "读取目录失败")
			return
		}
		directories := make([]M, 0)
		for _, entry := range entries {
			if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
				continue
			}
			directories = append(directories, M{
				"Name":     entry.Name(),
				"Relative": filepath.Join(scope.Relative, entry.Name()),
			})
		}
		respond(w, M{"Directories": directories})
		return
	}

	rows, err := a.db.Query(`SELECT lib,path,kind
			 FROM items
			 WHERE kind IN ('Movie','Series','Season','Episode')
			   AND path ILIKE ? ESCAPE '\'
			 ORDER BY path
			 LIMIT 200`, "%"+catalogLike(search)+"%")
	if err != nil {
		fail(w, http.StatusInternalServerError, "目录搜索失败")
		return
	}
	defer rows.Close()

	results := make([]M, 0)
	seen := make(map[string]struct{})
	for rows.Next() {
		var libraryID, itemPath, kind string
		if err := rows.Scan(&libraryID, &itemPath, &kind); err != nil {
			continue
		}

		candidatePath := itemPath
		if kind == "Movie" || kind == "Episode" {
			candidatePath = filepath.Dir(itemPath)
		}

		var libraryPath string
		if err := a.db.QueryRow("SELECT path FROM libraries WHERE id=?", libraryID).Scan(&libraryPath); err != nil {
			continue
		}
		for _, rootPath := range a.libraryPaths(libraryID, libraryPath) {
			relative, err := filepath.Rel(rootPath, candidatePath)
			if err != nil || (relative != "." && !filepath.IsLocal(relative)) {
				continue
			}
			key := libraryID + "\x00" + rootPath + "\x00" + relative
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			results = append(results, M{
				"Library":  libraryID,
				"Root":     rootPath,
				"Relative": relative,
				"Name":     filepath.Base(candidatePath),
			})
		}
	}
	respond(w, M{"Results": results})
}

// Record scope, recognition and progress fields on the activity.
func (a *App) scraperPhase(phase string, scope scraperScope, recognition MediaRecognition, scraper, reason string) {
	safe := func(value string) string {
		if value == "" {
			return ""
		}
		return a.scraperSafeError(errors.New(value)).Error()
	}
	details := M{
		"phase":      phase,
		"library":    safe(scope.Library),
		"directory":  safe(scope.Relative),
		"title":      safe(recognition.Title),
		"kind":       safe(recognition.Kind),
		"recognizer": safe(recognition.Recognizer),
		"scraper":    safe(scraper),
		"reason":     safe(reason),
	}

	payload, _ := json.Marshal(details)
	activityID := a.newActivity("scraper", "", phase)
	a.changeActivity(activityID, func(entry *activityEntry) {
		entry.Current = string(payload)
	})

	var finishErr error
	if strings.Contains(phase, "失败") || strings.Contains(phase, "未完成") {
		finishErr = errors.New(safe(reason))
	}
	a.finishActivity(activityID, finishErr)
}
