package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type mediaViewerKey struct{}

type mediaReader struct {
	db     *Database
	ctx    context.Context
	scope  string
	viewer User
}

func scopeMediaSQL(statement, scope string) string {
	if scope == "" {
		return statement
	}
	statement = strings.TrimSpace(statement)
	upper := strings.ToUpper(statement)
	if strings.HasPrefix(upper, "WITH") && len(statement) > 4 && strings.ContainsRune(" \t\r\n", rune(statement[4])) {
		rest := strings.TrimSpace(statement[4:])
		if strings.HasPrefix(strings.ToUpper(rest), "RECURSIVE") && len(rest) > 9 && strings.ContainsRune(" \t\r\n", rune(rest[9])) {
			return "WITH RECURSIVE " + scope + ", " + strings.TrimSpace(rest[9:])
		}
		return "WITH " + scope + ", " + rest
	}
	return "WITH " + scope + " " + statement
}

func (a *App) mediaForUser(ctx context.Context, user User) *mediaReader {
	reader := &mediaReader{db: a.db, ctx: ctx, viewer: user}
	if user.Admin || user.API {
		return reader
	}
	schema := a.mediaSchema
	if schema == "" {
		schema = `"public"`
	}
	permission := "hidden=0"
	userID := "'" + strings.ReplaceAll(user.ID, "'", "''") + "'"
	if user.ID != "" {
		permission += " OR EXISTS (SELECT 1 FROM " + schema + ".users WHERE id=" + userID + " AND can_view_hidden_libraries<>0)"
	}
	permission = "(" + permission + ") AND NOT EXISTS (SELECT 1 FROM " + schema + ".feature_library_policy policy WHERE policy.lib=libraries.id AND COALESCE(policy.data::jsonb->>'RestrictUsers','false')='true' AND NOT EXISTS (SELECT 1 FROM jsonb_array_elements_text(CASE WHEN jsonb_typeof(policy.data::jsonb->'Users')='array' THEN policy.data::jsonb->'Users' ELSE '[]'::jsonb END) AS allowed(value) WHERE allowed.value=" + userID + "))"
	reader.scope = "libraries AS NOT MATERIALIZED (SELECT * FROM " + schema + ".libraries WHERE " + permission + "), items AS NOT MATERIALIZED (SELECT * FROM " + schema + ".items WHERE lib IN (SELECT id FROM libraries))"
	for _, table := range []string{"item_people", "item_genres", "item_metadata", "media_probe", "media_display_names"} {
		reader.scope += ", " + table + " AS NOT MATERIALIZED (SELECT * FROM " + schema + "." + table + " WHERE item IN (SELECT id FROM items))"
	}
	reader.scope += ", covers AS NOT MATERIALIZED (SELECT * FROM " + schema + ".covers WHERE id IN (SELECT id FROM items UNION ALL SELECT id FROM libraries UNION ALL SELECT person FROM item_people))"
	return reader
}

func (a *App) mediaReader(r *http.Request) *mediaReader {
	user, _ := r.Context().Value(mediaViewerKey{}).(User)
	return a.mediaForUser(r.Context(), user)
}

func (d *mediaReader) Query(statement string, args ...any) (*sql.Rows, error) {
	return d.db.DB.QueryContext(d.ctx, bind(scopeMediaSQL(statement, d.scope)), pgArgs(args)...)
}

func (d *mediaReader) QueryRow(statement string, args ...any) *sql.Row {
	return d.db.DB.QueryRowContext(d.ctx, bind(scopeMediaSQL(statement, d.scope)), pgArgs(args)...)
}

func (a *App) itemForUser(r *http.Request, itemID string) (Item, error) {
	return readItem(a.mediaReader(r).QueryRow("SELECT "+cols+" FROM items WHERE id=?", canonicalPlaybackID(itemID)))
}

func (a *App) librarySettings(w http.ResponseWriter, r *http.Request) {
	a.libraryConfig.Lock()
	defer a.libraryConfig.Unlock()
	values := func() M {
		return M{"Schedule": a.getScanSchedule(), "ScanConcurrency": a.jobLimit(false), "UpdateConcurrency": a.jobLimit(true)}
	}
	if r.Method == http.MethodGet {
		respond(w, values())
		return
	}
	if r.Method != http.MethodPut {
		fail(w, 405, "PUT required")
		return
	}
	var input struct {
		Schedule                           *scanSchedule
		ScanConcurrency, UpdateConcurrency *int
	}
	if !body(w, r, &input) {
		return
	}
	for _, count := range []*int{input.ScanConcurrency, input.UpdateConcurrency} {
		if count != nil && (*count < 1 || *count > 64) {
			fail(w, 400, "并发数量范围 1–64")
			return
		}
	}
	if c := input.Schedule; c != nil {
		_, err := time.Parse("15:04", c.Time)
		if (c.Frequency != "daily" && c.Frequency != "weekly" && c.Frequency != "minutes") || err != nil || c.Weekday < 0 || c.Weekday > 6 || c.Minutes < 1 || c.Minutes > 10080 {
			fail(w, 400, "无效定时时间")
			return
		}
		c.Next = nextScan(*c, time.Now().UTC()).Unix()
	}
	tx, err := a.db.Begin()
	if err != nil {
		fail(w, 500, "保存失败")
		return
	}
	defer tx.Rollback()
	settings := map[string]string{}
	if input.ScanConcurrency != nil {
		settings["scan_concurrency"] = strconv.Itoa(*input.ScanConcurrency)
	}
	if input.UpdateConcurrency != nil {
		settings["update_concurrency"] = strconv.Itoa(*input.UpdateConcurrency)
	}
	if input.Schedule != nil {
		raw, err := json.Marshal(input.Schedule)
		if err != nil {
			fail(w, 500, "保存失败")
			return
		}
		settings["full_scan_schedule"] = string(raw)
	}
	for key, value := range settings {
		if _, err = tx.Exec("INSERT INTO settings(k,v) VALUES(?,?) ON CONFLICT(k) DO UPDATE SET v=excluded.v", key, value); err != nil {
			fail(w, 500, "保存失败")
			return
		}
	}
	if err = tx.Commit(); err != nil {
		fail(w, 500, "保存失败")
		return
	}
	a.wakeLibraryJobs()
	respond(w, values())
}

func (a *App) updateLibraryVisibility(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		fail(w, 405, "PUT required")
		return
	}
	var input struct {
		ID     string
		Name   *string
		Hidden *bool
	}
	if !body(w, r, &input) {
		return
	}
	if input.Name == nil && input.Hidden == nil {
		fail(w, 400, "缺少设置")
		return
	}
	var name, hidden any
	if input.Name != nil {
		value := strings.TrimSpace(*input.Name)
		if len(value) == 0 || len(value) > 256 {
			fail(w, 400, "名称需要 1–256 字节")
			return
		}
		name = value
	}
	if input.Hidden != nil {
		hidden = *input.Hidden
	}
	result, err := a.db.Exec("UPDATE libraries SET name=COALESCE(?,name),hidden=COALESCE(?,hidden) WHERE id=?", name, hidden, input.ID)
	if err != nil {
		fail(w, 500, "保存失败")
		return
	}
	count, err := result.RowsAffected()
	if err != nil {
		fail(w, 500, "保存失败")
		return
	}
	if count == 0 {
		fail(w, 404, "媒体库不存在")
		return
	}
	respond(w, M{"ok": true})
}

func (a *App) libraryVisibilitySchema() error {
	if _, err := a.db.Exec("ALTER TABLE libraries ADD COLUMN IF NOT EXISTS hidden BIGINT NOT NULL DEFAULT 0;\n ALTER TABLE users ADD COLUMN IF NOT EXISTS can_view_hidden_libraries BIGINT NOT NULL DEFAULT 0;\n CREATE TABLE IF NOT EXISTS feature_library_policy(lib TEXT PRIMARY KEY REFERENCES libraries(id) ON DELETE CASCADE,data TEXT NOT NULL);"); err != nil {
		return err
	}
	return a.db.QueryRow("SELECT quote_ident(current_schema())").Scan(&a.mediaSchema)
}

// Deny access if the user permission cannot be read.
func (a *App) canViewHiddenLibraries(userID string) bool {
	var allowed bool
	err := a.db.QueryRow("SELECT can_view_hidden_libraries FROM users WHERE id=?", userID).Scan(&allowed)
	return err == nil && allowed
}
