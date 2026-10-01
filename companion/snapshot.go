package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/lib/pq"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

type fileStamp struct{ Size, Mtime int64 }
type fileSnapshot map[string]fileStamp

func (a *App) previousSnapshot(lib string) (fileSnapshot, error) {

	var raw string
	result := fileSnapshot{}
	e := a.db.QueryRow("SELECT data FROM scan_snapshots WHERE lib=?", lib).Scan(&raw)
	if e != nil {
		if e == sql.ErrNoRows {
			return result, nil
		}
		return nil, e
	}
	e = json.Unmarshal([]byte(raw), &result)
	return result, e
}
func sidecarFile(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".nfo", ".jpg", ".jpeg", ".png", ".webp":
		return true
	}
	return false
}
func changedSidecars(old, current fileSnapshot) map[string]bool {
	dirs := map[string]bool{}
	for p, v := range current {
		if sidecarFile(p) && old[p] != v {
			dirs[filepath.Dir(p)] = true
		}
	}
	for p := range old {
		if sidecarFile(p) {
			if _, ok := current[p]; !ok {
				dirs[filepath.Dir(p)] = true
			}
		}
	}
	return dirs
}
func dependencyChanged(path string, dirs map[string]bool) bool {
	for d := filepath.Dir(path); d != "/" && d != "."; d = filepath.Dir(d) {
		if dirs[d] {
			return true
		}
	}
	return false
}
func (a *App) commitSnapshot(lib string, current fileSnapshot, allowEmpty bool, scopes []string) error {
	tx, e := a.db.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	// Use a temporary inventory to reconcile against the catalog, including a first scan.
	if _, e = tx.Exec("CREATE TEMP TABLE present_media(path TEXT PRIMARY KEY) ON COMMIT DROP"); e != nil {
		return e
	}
	stmt, e := tx.Tx.Prepare(pq.CopyIn("present_media", "path"))
	if e != nil {
		return e
	}
	count := 0
	for p := range current {
		if strings.EqualFold(filepath.Ext(p), ".strm") {
			count++
			if _, e = stmt.Exec(p); e != nil {
				stmt.Close()
				return e
			}
		}
	}
	if _, e = stmt.Exec(); e != nil {
		stmt.Close()
		return e
	}
	if e = stmt.Close(); e != nil {
		return e
	}
	if count == 0 && !allowEmpty {
		var n int
		if e = tx.QueryRow("SELECT count(*) FROM items WHERE lib=?", lib).Scan(&n); e != nil {
			return e
		}
		if n > 0 {
			return fmt.Errorf("目录扫描为空，保留原索引和快照以防挂载异常")
		}
	}
	filter := ""
	args := []any{lib}
	if len(scopes) > 0 {
		clauses := []string{}
		for _, p := range scopes {
			clauses = append(clauses, "(path=? OR substr(path,1,length(?))=?)")
			args = append(args, p, p+"/", p+"/")
		}
		filter = " AND (" + strings.Join(clauses, " OR ") + ")"
	}
	if _, e = tx.Exec("DELETE FROM items WHERE lib=? AND kind IN ('Movie','Episode') AND NOT EXISTS (SELECT 1 FROM present_media p WHERE p.path=items.path)"+filter, args...); e != nil {
		return e
	}
	for _, kind := range []string{"Season", "Series"} {
		if _, e = tx.Exec("DELETE FROM items WHERE lib=? AND kind=? AND NOT EXISTS (SELECT 1 FROM items child WHERE child.parent=items.id)", lib, kind); e != nil {
			return e
		}
	}
	raw, e := json.Marshal(current)
	if e != nil {
		return e
	}
	if _, e = tx.Exec("INSERT INTO scan_snapshots(lib,data) VALUES(?,?) ON CONFLICT(lib) DO UPDATE SET data=excluded.data", lib, string(raw)); e != nil {
		return e
	}
	return tx.Commit()
}

var seasonDirPattern = regexp.MustCompile(`(?i)^season[ _.-]*0*(\d+)$`)

func seriesDirectory(root, path string) string {
	dir := filepath.Dir(path)
	for p := dir; p != root && p != "/" && p != "."; p = filepath.Dir(p) {
		if seasonDirPattern.MatchString(filepath.Base(p)) {
			return filepath.Dir(p)
		}
		if st, e := os.Stat(filepath.Join(p, "tvshow.nfo")); e == nil && !st.IsDir() {
			return p
		}
	}
	rel, _ := filepath.Rel(root, dir)
	return filepath.Join(root, strings.Split(rel, string(filepath.Separator))[0])
}
func seasonPoster(path string, n int) string {
	candidates := []string{}
	for _, ext := range []string{".jpg", ".png", ".webp", ".jpeg"} {
		candidates = append(candidates, filepath.Join(path, "poster"+ext), filepath.Join(path, "folder"+ext), filepath.Join(filepath.Dir(path), fmt.Sprintf("season%02d-poster%s", n, ext)), filepath.Join(filepath.Dir(path), fmt.Sprintf("season%d-poster%s", n, ext)))
	}
	for _, p := range candidates {
		if st, e := os.Stat(p); e == nil && st.Mode().IsRegular() {
			return p
		}
	}
	return ""
}

func withinScanScopes(path string, scopes []string) bool {
	for _, p := range scopes {
		if path == p || strings.HasPrefix(path, p+"/") {
			return true
		}
	}
	return false
}
func seriesFallbackPoster(path string) string {
	entries, e := os.ReadDir(path)
	if e != nil {
		return ""
	}
	for _, d := range entries {
		if d.IsDir() {
			if m := seasonDirPattern.FindStringSubmatch(d.Name()); len(m) == 2 {
				n, _ := strconv.Atoi(m[1])
				if p := seasonPoster(filepath.Join(path, d.Name()), n); p != "" {
					return p
				}
			}
		}
	}
	return ""
}
