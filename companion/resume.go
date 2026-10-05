package main

import (
	"net/http"
	"strings"
	"time"
)

func resumeWinner(includePlayed bool) string {
	eligible := "progress.position>0 AND progress.played=0"
	order := ""
	if includePlayed {
		eligible = `((progress.position>0 AND progress.played=0) OR (progress.played=1 AND (
		 EXISTS (SELECT 1 FROM resume_activity ra WHERE ra.user_id=progress.user_id AND ra.item=i.id)
		 OR EXISTS (SELECT 1 FROM userdata_extra ue WHERE ue.user_id=progress.user_id AND ue.item=i.id AND ue.last_played<>''))))`
		order = "progress.played ASC, "
	}
	return `SELECT item FROM (
	 SELECT i.id AS item,row_number() OVER (PARTITION BY
	 CASE WHEN i.kind='Episode' AND p.kind='Season' THEN 'series:'||p.parent
	 WHEN i.kind='Episode' AND p.kind='Series' THEN 'series:'||p.id ELSE 'item:'||i.id END
	 ORDER BY ` + order + `(SELECT GREATEST(COALESCE(ra.updated,0),
	 CASE WHEN COALESCE(ue.last_played,'') ~ '^\d{4}-\d{2}-\d{2}T' THEN
	 (EXTRACT(EPOCH FROM ue.last_played::timestamptz)*1000000000)::bigint ELSE 0 END)
	 FROM userdata d LEFT JOIN resume_activity ra ON ra.user_id=d.user_id AND ra.item=d.item
	 LEFT JOIN userdata_extra ue ON ue.user_id=d.user_id AND ue.item=d.item
	 WHERE d.user_id=progress.user_id AND d.item=i.id) DESC,i.id DESC) AS rank
	 FROM items i JOIN userdata progress ON progress.item=i.id LEFT JOIN items p ON p.id=i.parent
	 WHERE progress.user_id=? AND ` + eligible + ` AND i.kind IN ('Movie','Episode')
	 AND NOT EXISTS (SELECT 1 FROM resume_hidden h WHERE h.user_id=progress.user_id AND h.item=i.id
	 AND h.updated>=COALESCE((SELECT updated FROM resume_activity WHERE user_id=progress.user_id AND item=i.id),0))
	 ) ranked WHERE rank=1`
}

func resumeUpdatedExpression(userID string) string {
	user := "'" + strings.ReplaceAll(userID, "'", "''") + "'"
	return `GREATEST(COALESCE((SELECT updated FROM resume_activity WHERE user_id=` + user + ` AND item=items.id),0),
	 COALESCE((SELECT CASE WHEN last_played ~ '^\d{4}-\d{2}-\d{2}T' THEN
	 (EXTRACT(EPOCH FROM last_played::timestamptz)*1000000000)::bigint ELSE 0 END
	 FROM userdata_extra WHERE user_id=` + user + ` AND item=items.id),0))`
}

func resumePlayedExpression(userID string) string {
	return "COALESCE((SELECT played FROM userdata WHERE user_id='" + strings.ReplaceAll(userID, "'", "''") + "' AND item=items.id),0)"
}

func (a *App) resumeSeriesID(item Item) string {
	if item.Kind == "Series" {
		return item.ID
	}
	if item.Kind != "Episode" && item.Kind != "Season" {
		return ""
	}
	parent, err := a.item(item.Parent)
	if err != nil {
		return ""
	}
	if parent.Kind == "Series" {
		return parent.ID
	}
	if parent.Kind == "Season" {
		return parent.Parent
	}
	return ""
}

func (a *App) resumeDeleteRoute(w http.ResponseWriter, r *http.Request, user User, path string) bool {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) != 2 && len(parts) != 3 {
		return false
	}
	if !strings.EqualFold(parts[0], "items") || !strings.EqualFold(parts[len(parts)-1], "resume") {
		return false
	}
	if r.Method != http.MethodDelete {
		fail(w, 405, "DELETE required")
		return true
	}
	if user.API || user.ID == "" {
		fail(w, 403, "请使用登录用户账号")
		return true
	}
	query := "UPDATE userdata SET position=0 WHERE user_id=? AND played=0 AND position>0 AND item IN ("
	args := []any{user.ID}
	if len(parts) == 2 {
		query += "SELECT id FROM items WHERE kind IN ('Movie','Episode'))"
	} else {
		item, err := a.itemForUser(r, parts[1])
		if err != nil || (item.Kind != "Movie" && item.Kind != "Episode" && item.Kind != "Series") {
			fail(w, 404, "媒体不存在")
			return true
		}
		if item.Kind == "Series" {
			query += "SELECT i.id FROM items i LEFT JOIN items p ON p.id=i.parent WHERE i.kind='Episode' AND (i.parent=? OR (p.kind='Season' AND p.parent=?)))"
			args = append(args, item.ID, item.ID)
		} else {
			query += "SELECT id FROM items WHERE id=?)"
			args = append(args, item.ID)
		}
	}
	if _, err := a.db.Exec(scopeMediaSQL(query, a.mediaReader(r).scope), args...); err != nil {
		fail(w, 500, "删除继续播放记录失败")
		return true
	}
	w.WriteHeader(http.StatusNoContent)
	return true
}

func (a *App) resumeHideRoute(w http.ResponseWriter, r *http.Request, user User, path string) bool {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) != 5 || !strings.EqualFold(parts[0], "users") || !strings.EqualFold(parts[2], "items") || !strings.EqualFold(parts[4], "hidefromresume") {
		return false
	}
	if r.Method != http.MethodPost {
		fail(w, 405, "POST required")
		return true
	}
	if user.API || user.ID == "" || (parts[1] != "me" && parts[1] != user.ID) {
		fail(w, 403, "请使用当前登录用户账号")
		return true
	}
	hideValue := q(r, "Hide")
	if !strings.EqualFold(hideValue, "true") && !strings.EqualFold(hideValue, "false") {
		fail(w, 400, "Hide must be true or false")
		return true
	}
	item, err := a.itemForUser(r, parts[3])
	if err != nil || (item.Kind != "Movie" && item.Kind != "Episode" && item.Kind != "Series") {
		fail(w, 404, "媒体不存在")
		return true
	}
	ids := []string{item.ID}
	if series := a.resumeSeriesID(item); series != "" {
		rows, err := a.mediaReader(r).Query("SELECT i.id FROM items i LEFT JOIN items p ON p.id=i.parent WHERE i.kind='Episode' AND (i.parent=? OR (p.kind='Season' AND p.parent=?))", series, series)
		if err != nil {
			fail(w, 500, "读取继续播放记录失败")
			return true
		}
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				break
			}
			ids = append(ids, id)
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			fail(w, 500, "读取继续播放记录失败")
			return true
		}
	}
	tx, err := a.db.Begin()
	if err != nil {
		fail(w, 500, "保存失败")
		return true
	}
	defer tx.Rollback()
	for _, id := range ids {
		if strings.EqualFold(hideValue, "true") {
			_, err = tx.Exec("INSERT INTO resume_hidden(user_id,item,updated) VALUES(?,?,?) ON CONFLICT(user_id,item) DO UPDATE SET updated=excluded.updated", user.ID, id, time.Now().UnixNano())
		} else {
			_, err = tx.Exec("DELETE FROM resume_hidden WHERE user_id=? AND item=?", user.ID, id)
		}
		if err != nil {
			fail(w, 500, "保存失败")
			return true
		}
	}
	if err = tx.Commit(); err != nil {
		fail(w, 500, "保存失败")
		return true
	}
	state, err := a.readUserState(user.ID, item.ID)
	if err != nil {
		fail(w, 500, "读取播放状态失败")
		return true
	}
	respond(w, state)
	return true
}
