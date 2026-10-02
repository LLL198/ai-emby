package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/lib/pq"
)

type mediaIssue struct {
	ID        string `json:"id"`
	Source    string `json:"source"`
	Path      string `json:"path"`
	Status    string `json:"status"`
	Reason    string `json:"reason"`
	Proposed  string `json:"proposed"`
	Directory bool   `json:"directory"`
	Kind      string `json:"kind"`
	ItemID    string `json:"item_id"`
	Ignored   bool   `json:"ignored"`
	Updated   int64  `json:"updated"`
}

func (a *App) storeMediaIssues(ctx context.Context, source string, issues []mediaIssue, resolved []string, observed int64) error {
	if a.db == nil || len(issues)+len(resolved) == 0 {
		return nil
	}
	secret := a.tmdbSettings().APIKey
	tx, err := a.db.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if len(resolved) > 0 {
		if _, err = tx.ExecContext(ctx, "DELETE FROM feature_media_issues WHERE source=$1 AND path=ANY($2) AND updated<=$3", source, pq.Array(resolved), observed); err != nil {
			return err
		}
	}
	for offset := 0; offset < len(issues); offset += 500 {
		end := min(offset+500, len(issues))
		for i := offset; i < end; i++ {
			issues[i].ID = id()
			issues[i].Source = source
			issues[i].Updated = observed
			issues[i].Reason = scraperSanitize(issues[i].Reason, secret)
		}
		data, err := json.Marshal(issues[offset:end])
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO feature_media_issues(id,source,path,status,reason,proposed,directory,kind,item_id,created,updated)
SELECT id,source,path,status,reason,proposed,directory,kind,item_id,updated,updated FROM jsonb_to_recordset($1::jsonb) AS i(id text,source text,path text,status text,reason text,proposed text,directory boolean,kind text,item_id text,updated bigint)
ON CONFLICT(source,path) DO UPDATE SET status=excluded.status,reason=excluded.reason,proposed=excluded.proposed,directory=excluded.directory,kind=excluded.kind,item_id=excluded.item_id,updated=excluded.updated WHERE feature_media_issues.updated<=excluded.updated`, string(data))
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (a *App) namingStoreIssues(ctx context.Context, plan *namingPlan, request namingRequest) error {
	issues := []mediaIssue{}
	resolved := []string{}
	for _, row := range plan.Rows {
		if (row.Status == "review" || row.Status == "conflict") && !(row.Directory && row.Reason == "未找到媒体文件或季目录，请进入具体作品目录") {
			issues = append(issues, mediaIssue{Path: row.Old, Status: row.Status, Reason: row.Reason, Proposed: row.New, Directory: row.Directory})
		} else if row.Status == "unchanged" && request.Mode == "auto" || row.Status == "renamed" || row.Directory && row.Reason == "未找到媒体文件或季目录，请进入具体作品目录" {
			resolved = append(resolved, row.Old)
		}
	}
	return a.storeMediaIssues(ctx, "naming", issues, resolved, time.Now().UnixNano())
}

func (a *App) scraperStoreIssue(ctx context.Context, object scraperObject, outcome error) {
	if a.features.ctx == nil || ctx.Err() != nil {
		return
	}
	path, err := filepath.Rel(fileRoot(), object.Item.Path)
	if err != nil || !filepath.IsLocal(path) {
		return
	}
	attempted := outcome != nil
	for _, target := range object.Targets {
		if target.Action != "skip" {
			attempted = true
		}
	}
	if !attempted {
		return
	}
	issues := []mediaIssue{}
	resolved := []string{}
	if outcome != nil {
		status := "failed"
		if strings.Contains(outcome.Error(), "TMDB 无此单集") {
			status = "review"
		}
		issues = append(issues, mediaIssue{Path: path, Status: status, Reason: outcome.Error(), Kind: object.Kind, ItemID: object.ID, Directory: object.Kind == "Series" || object.Kind == "Season"})
	} else {
		resolved = append(resolved, path)
	}
	persistCtx, cancel := context.WithTimeout(a.features.ctx, 10*time.Second)
	defer cancel()
	if err := a.storeMediaIssues(persistCtx, "scraper", issues, resolved, time.Now().UnixNano()); err != nil {
		a.scraperPhase("待处理记录失败", scraperScope{}, MediaRecognition{}, "", "无法保存待处理项目，请查看单片日志")
	}
}

func (a *App) scraperIssueItems(ctx context.Context, ids []string) ([]Item, int, error) {
	rows, err := a.db.DB.QueryContext(ctx, "SELECT id,path,kind,item_id FROM feature_media_issues WHERE source='scraper' AND id=ANY($1)", pq.Array(ids))
	if err != nil {
		return nil, 0, errors.New("读取待处理项目失败")
	}
	issues := make(map[string]mediaIssue, len(ids))
	for rows.Next() {
		var issue mediaIssue
		if err := rows.Scan(&issue.ID, &issue.Path, &issue.Kind, &issue.ItemID); err != nil {
			rows.Close()
			return nil, 0, errors.New("读取待处理项目失败")
		}
		issues[issue.ID] = issue
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, 0, errors.New("读取待处理项目失败")
	}
	items := make([]Item, 0, len(issues))
	seen := make(map[string]bool, len(issues))
	skipped := 0
	for _, issueID := range ids {
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
		issue, exists := issues[issueID]
		if !exists || issue.ItemID == "" || !filepath.IsLocal(issue.Path) {
			skipped++
			continue
		}
		item, err := readItem(a.db.QueryRowContext(ctx, "SELECT "+cols+" FROM items WHERE id=$1", issue.ItemID))
		if errors.Is(err, sql.ErrNoRows) {
			skipped++
			continue
		}
		if err != nil {
			return nil, 0, errors.New("读取媒体索引失败")
		}
		if item.Kind != issue.Kind || item.Path != filepath.Join(fileRoot(), issue.Path) || len(scraperCategoryContents[item.Kind]) == 0 || seen[item.ID] {
			skipped++
			continue
		}
		seen[item.ID] = true
		items = append(items, item)
	}
	return items, skipped, nil
}

func namingRebaseIssues(ctx context.Context, tx *sql.Tx, moves []namingMove, mapping namingPathMap) error {
	paths, dirs := []string{}, []string{}
	for _, move := range namingCatalogMoves(moves) {
		paths = append(paths, move.Old)
		if move.Info.IsDir() {
			dirs = append(dirs, move.Old)
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,path,proposed FROM feature_media_issues WHERE path=ANY($1) OR EXISTS(SELECT 1 FROM unnest($2::text[]) AS roots(root) WHERE left(path,length(root)+1)=root||'/') FOR UPDATE NOWAIT`, pq.Array(paths), pq.Array(dirs))
	if err != nil {
		return errors.New("待处理记录正在更新，请稍后重新预览")
	}
	type change struct {
		ID       string `json:"id"`
		Path     string `json:"path"`
		Proposed string `json:"proposed"`
	}
	changes := []change{}
	for rows.Next() {
		var row change
		if err = rows.Scan(&row.ID, &row.Path, &row.Proposed); err != nil {
			rows.Close()
			return err
		}
		next, err := filepath.Rel(fileRoot(), mapping.rebase(filepath.Join(fileRoot(), row.Path)))
		if err != nil || !filepath.IsLocal(next) {
			rows.Close()
			return errors.New("待处理项目路径无效")
		}
		row.Path = next
		if row.Proposed != "" {
			row.Proposed, _ = filepath.Rel(fileRoot(), mapping.rebase(filepath.Join(fileRoot(), row.Proposed)))
		}
		changes = append(changes, row)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if len(changes) > 0 {
		data, _ := json.Marshal(changes)
		if _, err = tx.ExecContext(ctx, `DELETE FROM feature_media_issues stale USING jsonb_to_recordset($1::jsonb) AS m(id text,path text) JOIN feature_media_issues old ON old.id=m.id WHERE stale.source=old.source AND stale.path=m.path AND stale.id<>old.id`, string(data)); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE feature_media_issues i SET path=m.path,proposed=m.proposed FROM jsonb_to_recordset($1::jsonb) AS m(id text,path text,proposed text) WHERE i.id=m.id`, string(data)); err != nil {
			return err
		}
	}
	resolved := []string{}
	for _, move := range moves {
		next, err := filepath.Rel(fileRoot(), mapping.rebase(filepath.Join(fileRoot(), move.Old)))
		if err != nil {
			return err
		}
		resolved = append(resolved, next)
	}
	_, err = tx.ExecContext(ctx, "DELETE FROM feature_media_issues WHERE source='naming' AND path=ANY($1)", pq.Array(resolved))
	return err
}

func (a *App) mediaIssuesAPI(w http.ResponseWriter, r *http.Request) {
	if !featureMethod(w, r, http.MethodGet, http.MethodPut) {
		return
	}
	if r.Method == http.MethodPut {
		var request struct {
			ID      string   `json:"id"`
			IDs     []string `json:"ids"`
			Ignored bool     `json:"ignored"`
		}
		if !body(w, r, &request) {
			return
		}
		if request.ID != "" && len(request.IDs) != 0 {
			fail(w, 400, "请使用单项或批量选择")
			return
		}
		ids := request.IDs
		if request.ID != "" {
			ids = []string{request.ID}
		}
		if len(ids) == 0 || len(ids) > 100 {
			fail(w, 400, "请选择1–100个待处理项目")
			return
		}
		unique := make([]string, 0, len(ids))
		seen := make(map[string]bool, len(ids))
		for _, issueID := range ids {
			if len(issueID) != 32 {
				fail(w, 400, "无效待处理项目")
				return
			}
			if !seen[issueID] {
				seen[issueID] = true
				unique = append(unique, issueID)
			}
		}
		result, err := a.db.DB.ExecContext(r.Context(), "UPDATE feature_media_issues SET ignored=$1 WHERE id=ANY($2)", request.Ignored, pq.Array(unique))
		if err != nil {
			featureError(w, err)
			return
		}
		count, _ := result.RowsAffected()
		if count == 0 {
			fail(w, 404, "项目已处理或已移除")
			return
		}
		respond(w, M{"ok": true, "updated": count, "missing": int64(len(unique)) - count})
		return
	}
	query := r.URL.Query()
	search := strings.TrimSpace(query.Get("search"))
	source, status, state := query.Get("source"), query.Get("status"), query.Get("state")
	if len(search) > 512 || source != "" && source != "naming" && source != "scraper" || status != "" && status != "review" && status != "conflict" && status != "failed" || state != "" && state != "ignored" && state != "all" {
		fail(w, 400, "筛选条件无效")
		return
	}
	page, _ := strconv.Atoi(query.Get("page"))
	page = max(1, min(page, 100000))
	where := ` WHERE ($1='' OR source=$1) AND ($2='' OR status=$2) AND ($3='all' OR ignored=($3='ignored')) AND ($4='' OR strpos(lower(path||' '||reason),lower($4))>0)`
	args := []any{source, status, state, search}
	var total, pending int
	if err := a.db.DB.QueryRowContext(r.Context(), "SELECT count(*) FROM feature_media_issues"+where, args...).Scan(&total); err != nil {
		featureError(w, err)
		return
	}
	if err := a.db.DB.QueryRowContext(r.Context(), "SELECT count(*) FROM feature_media_issues WHERE NOT ignored").Scan(&pending); err != nil {
		featureError(w, err)
		return
	}
	if query.Get("summary") == "true" {
		respond(w, M{"pending": pending, "total": total})
		return
	}
	rows, err := a.db.DB.QueryContext(r.Context(), `SELECT id,source,path,status,reason,proposed,directory,kind,item_id,ignored,updated FROM feature_media_issues`+where+` ORDER BY updated DESC,id LIMIT 100 OFFSET $5`, append(args, (page-1)*100)...)
	if err != nil {
		featureError(w, err)
		return
	}
	defer rows.Close()
	items := []mediaIssue{}
	for rows.Next() {
		var issue mediaIssue
		if err = rows.Scan(&issue.ID, &issue.Source, &issue.Path, &issue.Status, &issue.Reason, &issue.Proposed, &issue.Directory, &issue.Kind, &issue.ItemID, &issue.Ignored, &issue.Updated); err != nil {
			featureError(w, err)
			return
		}
		items = append(items, issue)
	}
	if err = rows.Err(); err != nil {
		featureError(w, err)
		return
	}
	respond(w, M{"items": items, "total": total, "pending": pending, "page": page, "pageSize": 100})
}
