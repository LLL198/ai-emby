package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
)

type namingLibraryScope struct {
	Library string
	Root    string
	Rules   []scraperScope
}

func (a *App) namingScraperRoots() ([]namingLibraryScope, string, error) {
	config := a.scraperSettings()
	roots := []namingLibraryScope{}
	for _, lib := range a.libraries() {
		for _, location := range lib["Locations"].([]string) {
			relative, err := filepath.Rel(fileRoot(), location)
			if err != nil || !filepath.IsLocal(relative) {
				return nil, "", errors.New("媒体库目录必须位于文件管理的媒体根目录内")
			}
			scope := namingLibraryScope{Library: lib["Id"].(string), Root: location, Rules: []scraperScope{}}
			for _, rule := range config.ManualScopes {
				if rule.Library == scope.Library && rule.Root == scope.Root {
					scope.Rules = append(scope.Rules, rule)
				}
			}
			roots = append(roots, scope)
		}
	}
	if len(roots) == 0 {
		return nil, "", errors.New("请先添加媒体库并选择任务目录")
	}
	raw, _ := json.Marshal(roots)
	return roots, digest(string(raw)), nil
}

// Excluded branches are traversed only to reach explicitly enabled descendants.
func (request namingRequest) scopeAccess(path string, directory bool) (include, descend bool) {
	if !request.UseScraperScopes {
		return true, true
	}
	full := filepath.Join(fileRoot(), path)
	for _, root := range request.ScopeRoots {
		relative, err := filepath.Rel(root.Root, full)
		if err != nil || !filepath.IsLocal(relative) {
			continue
		}
		match := relative
		if !directory {
			match = filepath.Dir(relative)
		}
		enabled := scraperScopeEnabled(root.Rules, root.Library, root.Root, match, true)
		include = include || enabled
		descend = descend || enabled
		if directory {
			for _, rule := range root.Rules {
				if rule.Library == root.Library && rule.Root == root.Root && rule.Enabled && (relative == "." || strings.HasPrefix(rule.Relative, relative+"/")) {
					descend = true
				}
			}
		}
	}
	// Moving an ancestor would also move excluded descendants. Keep it in place.
	if directory && include {
		for _, root := range request.ScopeRoots {
			for _, rule := range root.Rules {
				if rule.Library != root.Library || rule.Root != root.Root || rule.Enabled {
					continue
				}
				excluded := filepath.Join(rule.Root, rule.Relative)
				if strings.HasPrefix(excluded, full+string(filepath.Separator)) {
					include = false
				}
			}
		}
	}
	return
}

func namingRebaseScraperScopes(ctx context.Context, tx *sql.Tx, mapping namingPathMap) error {
	var raw []byte
	err := tx.QueryRowContext(ctx, "SELECT v FROM settings WHERE k='scraper' FOR UPDATE NOWAIT").Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return errors.New("刮削设置正在修改，请稍后重新预览")
	}
	var settings map[string]json.RawMessage
	if json.Unmarshal(raw, &settings) != nil {
		return errors.New("无法读取刮削目录设置，未改动文件")
	}
	changed := false
	for _, key := range []string{"ManualScopes", "MonitorScopes"} {
		if len(settings[key]) == 0 {
			continue
		}
		var rules []map[string]json.RawMessage
		if json.Unmarshal(settings[key], &rules) != nil {
			return errors.New("无法解析刮削目录规则，未改动文件")
		}
		for _, rule := range rules {
			var root, relative string
			if json.Unmarshal(rule["Root"], &root) != nil || json.Unmarshal(rule["Relative"], &relative) != nil {
				return errors.New("刮削目录规则不完整，未改动文件")
			}
			full := filepath.Join(root, relative)
			next := mapping.rebase(full)
			if next != full {
				rebased, err := filepath.Rel(root, next)
				if err != nil || !filepath.IsLocal(rebased) {
					return errors.New("改名后的目录超出媒体库范围")
				}
				rule["Relative"], _ = json.Marshal(rebased)
				changed = true
			}
		}
		settings[key], _ = json.Marshal(rules)
	}
	if changed {
		data, _ := json.Marshal(settings)
		_, err = tx.ExecContext(ctx, "UPDATE settings SET v=$1 WHERE k='scraper'", string(data))
	}
	return err
}

func (a *App) namingRefresh(w http.ResponseWriter, r *http.Request) {
	if !featureMethod(w, r, http.MethodPost) {
		return
	}
	var request struct {
		ScopeVersion string `json:"scopeVersion"`
		CheckOnly    bool   `json:"checkOnly"`
	}
	if !body(w, r, &request) {
		return
	}
	roots, version, err := a.namingScraperRoots()
	if err != nil || version != request.ScopeVersion {
		fail(w, 409, "任务目录已变化，请重新选择范围并生成预览")
		return
	}
	ids := []string{}
	seen := map[string]bool{}
	for _, root := range roots {
		path, _ := filepath.Rel(fileRoot(), root.Root)
		if _, descend := (namingRequest{UseScraperScopes: true, ScopeRoots: []namingLibraryScope{root}}).scopeAccess(path, true); !descend || seen[root.Library] {
			continue
		}
		seen[root.Library] = true
		if !request.CheckOnly {
			if _, reserved := a.reserveConcurrentScan(root.Library); reserved {
				go a.runConcurrentScan(root.Library, false, false, nil)
			}
		}
		ids = append(ids, root.Library)
	}
	respond(w, M{"Libraries": ids, "scopeVersion": version})
}
