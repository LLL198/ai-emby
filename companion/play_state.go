package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strings"
	"time"
)

func catalogUserState(itemID string, position int64, played, favorite bool, count int64, lastPlayed string, duration float64) M {
	state := M{"ItemId": itemID, "Key": itemID, "PlaybackPositionTicks": position, "Played": played, "IsFavorite": favorite, "PlayCount": count}
	if lastPlayed != "" {
		state["LastPlayedDate"] = lastPlayed
	}
	if duration > 0 {
		state["PlayedPercentage"] = math.Min(100, math.Max(0, float64(position)/duration*100))
	}
	return state
}

func (a *App) readUserState(userID, itemID string) (M, error) {
	var position, count int64
	var played, favorite bool
	var last string
	err := a.db.QueryRow(`SELECT COALESCE(d.position,0),COALESCE(d.played,0),COALESCE(e.favorite,0),COALESCE(e.play_count,0),COALESCE(e.last_played,'')
 FROM items i LEFT JOIN userdata d ON d.item=i.id AND d.user_id=? LEFT JOIN userdata_extra e ON e.item=i.id AND e.user_id=? WHERE i.id=?`, userID, userID, itemID).Scan(&position, &played, &favorite, &count, &last)
	return catalogUserState(itemID, position, played, favorite, count, last, 0), err
}

func catalogDTOFloat(value any) float64 {
	switch n := value.(type) {
	case float64:
		return n
	case float32:
		return float64(n)
	case int:
		return float64(n)
	case int64:
		return float64(n)
	case json.Number:
		v, _ := n.Float64()
		return v
	}
	return 0
}

func (a *App) fillPlayState(r *http.Request, user User, dtos []M) error {
	if user.API || len(dtos) == 0 || strings.EqualFold(q(r, "EnableUserData"), "false") {
		return nil
	}
	positions := map[string][]int{}
	args := []any{user.ID, user.ID}
	for i, dto := range dtos {
		itemID, ok := dto["Id"].(string)
		if !ok || itemID == "" {
			continue
		}
		if _, exists := positions[itemID]; !exists {
			args = append(args, itemID)
		}
		positions[itemID] = append(positions[itemID], i)
	}
	if len(positions) == 0 {
		return nil
	}
	rows, err := a.mediaReader(r).Query(`SELECT i.id,COALESCE(d.position,0),COALESCE(d.played,0),COALESCE(e.favorite,0),COALESCE(e.play_count,0),COALESCE(e.last_played,'')
 FROM items i LEFT JOIN userdata d ON d.item=i.id AND d.user_id=? LEFT JOIN userdata_extra e ON e.item=i.id AND e.user_id=? WHERE i.id IN (`+catalogPlaceholders(len(args)-2)+")", args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	states := map[string]M{}
	for rows.Next() {
		var itemID, last string
		var position, count int64
		var played, favorite bool
		if err := rows.Scan(&itemID, &position, &played, &favorite, &count, &last); err != nil {
			return err
		}
		states[itemID] = catalogUserState(itemID, position, played, favorite, count, last, 0)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for itemID, indices := range positions {
		state, exists := states[itemID]
		if !exists {
			continue
		}
		for _, i := range indices {
			copy := M{}
			for k, v := range state {
				copy[k] = v
			}
			if ticks := catalogDTOFloat(dtos[i]["RunTimeTicks"]); ticks > 0 {
				copy["PlayedPercentage"] = math.Min(100, float64(copy["PlaybackPositionTicks"].(int64))/ticks*100)
			}
			dtos[i]["UserData"] = copy
		}
	}
	return nil
}

func (a *App) playStateRoute(w http.ResponseWriter, r *http.Request, user User, path string) bool {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) != 4 && len(parts) != 5 {
		return false
	}
	if !strings.EqualFold(parts[0], "users") {
		return false
	}
	action, itemID := "", ""
	if len(parts) == 5 && strings.EqualFold(parts[2], "items") && strings.EqualFold(parts[4], "userdata") {
		action, itemID = "userdata", parts[3]
	}
	if len(parts) == 4 && (strings.EqualFold(parts[2], "playeditems") || strings.EqualFold(parts[2], "favoriteitems")) {
		action, itemID = strings.ToLower(parts[2]), parts[3]
	}
	if action == "" {
		return false
	}
	uid := parts[1]
	if uid == "me" {
		uid = user.ID
	}
	if uid == "" || (!user.Admin && !user.API && uid != user.ID) {
		fail(w, 403, "User playback state requires an authorized user")
		return true
	}
	var exists int
	if a.db.QueryRow("SELECT 1 FROM users WHERE id=?", uid).Scan(&exists) != nil {
		fail(w, 404, "User not found")
		return true
	}
	item, err := a.itemForUser(r, itemID)
	if err != nil {
		fail(w, 404, "Item not found")
		return true
	}
	if action == "userdata" && r.Method == http.MethodGet {
		state, err := a.readUserState(uid, item.ID)
		if err != nil {
			fail(w, 500, "Could not read playback state")
		} else {
			respond(w, state)
		}
		return true
	}
	var input struct {
		PlaybackPositionTicks *clientTicks
		Played, IsFavorite    *bool
		PlayCount             *int64
		LastPlayedDate        *string
	}
	switch action {
	case "userdata":
		if r.Method != http.MethodPost && r.Method != http.MethodPut {
			fail(w, 405, "Unsupported method")
			return true
		}
		if !body(w, r, &input) {
			return true
		}
		if input.PlaybackPositionTicks == nil && input.Played == nil && input.IsFavorite == nil && input.PlayCount == nil && input.LastPlayedDate == nil {
			fail(w, 400, "No supported user-data fields")
			return true
		}
	case "playeditems":
		if r.Method != http.MethodPost && r.Method != http.MethodDelete {
			fail(w, 405, "Unsupported method")
			return true
		}
		played := r.Method == http.MethodPost
		input.Played = &played
		zero := clientTicks(0)
		input.PlaybackPositionTicks = &zero
		date := ""
		if played {
			at := time.Now().UTC()
			if value := q(r, "DatePlayed"); value != "" {
				at, err = time.Parse("20060102150405", value)
				if err != nil {
					at, err = time.Parse(time.RFC3339Nano, value)
				}
				if err != nil {
					fail(w, 400, "Invalid DatePlayed")
					return true
				}
			}
			date = at.UTC().Format(time.RFC3339Nano)
		}
		input.LastPlayedDate = &date
	case "favoriteitems":
		if r.Method != http.MethodPost && r.Method != http.MethodDelete {
			fail(w, 405, "Unsupported method")
			return true
		}
		favorite := r.Method == http.MethodPost
		input.IsFavorite = &favorite
	}
	if (input.PlaybackPositionTicks != nil && *input.PlaybackPositionTicks < 0) || (input.PlayCount != nil && *input.PlayCount < 0) {
		fail(w, 400, "Invalid playback value")
		return true
	}
	if input.LastPlayedDate != nil && *input.LastPlayedDate != "" {
		at, err := time.Parse(time.RFC3339Nano, *input.LastPlayedDate)
		if err != nil {
			fail(w, 400, "Invalid LastPlayedDate")
			return true
		}
		*input.LastPlayedDate = at.UTC().Format(time.RFC3339Nano)
	}
	tx, err := a.db.Begin()
	if err != nil {
		fail(w, 500, "Could not save playback state")
		return true
	}
	defer tx.Rollback()
	var position, played, favorite, count, last any
	if input.PlaybackPositionTicks != nil {
		position = int64(*input.PlaybackPositionTicks)
	}
	if input.Played != nil {
		played = *input.Played
	}
	if input.IsFavorite != nil {
		favorite = *input.IsFavorite
	}
	if input.PlayCount != nil {
		count = *input.PlayCount
	}
	if input.LastPlayedDate != nil {
		last = *input.LastPlayedDate
	}
	_, err = tx.Exec(`INSERT INTO userdata(user_id,item,position,played) VALUES(?,?,COALESCE(CAST(? AS BIGINT),0),COALESCE(CAST(? AS BIGINT),0))
 ON CONFLICT(user_id,item) DO UPDATE SET position=COALESCE(?,userdata.position),played=COALESCE(?,userdata.played)`, uid, item.ID, position, played, position, played)
	if err == nil {
		_, err = tx.Exec(`INSERT INTO userdata_extra(user_id,item,favorite,play_count,last_played) VALUES(?,?,COALESCE(CAST(? AS BIGINT),0),COALESCE(CAST(? AS BIGINT),0),COALESCE(CAST(? AS TEXT),''))
 ON CONFLICT(user_id,item) DO UPDATE SET favorite=COALESCE(?,userdata_extra.favorite),play_count=COALESCE(?,userdata_extra.play_count),last_played=COALESCE(?,userdata_extra.last_played)`, uid, item.ID, favorite, count, last, favorite, count, last)
	}
	if err == nil && action == "playeditems" && r.Method == http.MethodPost && input.PlayCount == nil {
		_, err = tx.Exec("UPDATE userdata_extra SET play_count=play_count+1 WHERE user_id=? AND item=?", uid, item.ID)
	}
	if err == nil && (position != nil || played != nil) {
		if action == "playeditems" && r.Method == http.MethodDelete {
			_, err = tx.Exec("DELETE FROM resume_activity WHERE user_id=? AND item=?", uid, item.ID)
		} else {
			_, err = tx.Exec("INSERT INTO resume_activity VALUES(?,?,?) ON CONFLICT(user_id,item) DO UPDATE SET updated=excluded.updated", uid, item.ID, time.Now().UnixNano())
		}
	}
	if err == nil && input.IsFavorite != nil {
		_, err = tx.Exec("INSERT INTO settings(k,v) VALUES(?,?) ON CONFLICT(k) DO UPDATE SET v=excluded.v", "favorite-cover-rev:"+digest(uid), time.Now().UTC().Format(time.RFC3339Nano))
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		fail(w, 500, "Could not save playback state")
		return true
	}
	state, err := a.readUserState(uid, item.ID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		fail(w, 500, "Could not read playback state")
		return true
	}
	respond(w, state)
	return true
}
