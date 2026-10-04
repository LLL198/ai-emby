package main

import (
	"net/http"
	"strings"
)

// Select the first unplayed episode after the last played episode in each series.
func (a *App) nextUp(w http.ResponseWriter, r *http.Request, user User) {
	seriesID := q(r, "SeriesId")
	rows, err := a.mediaReader(r).Query(`SELECT i.id, CASE WHEN p.kind='Season' THEN p.parent ELSE p.id END AS series,
 COALESCE(d.played,0) FROM items i JOIN items p ON p.id=i.parent
 LEFT JOIN userdata d ON d.user_id=? AND d.item=i.id
 WHERE i.kind='Episode' AND (p.kind='Season' OR p.kind='Series')
 AND (?='' OR CASE WHEN p.kind='Season' THEN p.parent ELSE p.id END=?)
 ORDER BY series,i.season,i.episode,i.id`, user.ID, seriesID, seriesID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "读取下一集失败")
		return
	}
	ids := []string{}
	currentSeries, candidate := "", ""
	seenPlayed := false
	for rows.Next() {
		var itemID, series string
		var played int
		if err = rows.Scan(&itemID, &series, &played); err != nil {
			break
		}
		if currentSeries != "" && currentSeries != series {
			if seenPlayed && candidate != "" && len(ids) < 200 {
				ids = append(ids, candidate)
			}
			seenPlayed, candidate = false, ""
		}
		if played != 0 {
			seenPlayed, candidate = true, ""
		} else if seenPlayed && candidate == "" {
			candidate = itemID
		}
		currentSeries = series
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		fail(w, http.StatusInternalServerError, "读取下一集失败")
		return
	}
	if seenPlayed && candidate != "" && len(ids) < 200 {
		ids = append(ids, candidate)
	}
	if len(ids) != 0 {
		setQuery(r, "Ids", strings.Join(ids, ","))
		setQuery(r, "IncludeItemTypes", "Episode")
		a.items(w, r, user, false)
		return
	}
	respond(w, M{"Items": []M{}, "TotalRecordCount": 0, "StartIndex": 0, "HasMore": false})
}
