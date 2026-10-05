package main

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

func flatSeasonID(series string, season int) string {
	return "season-" + series + "-" + strconv.Itoa(season)
}

func parseFlatSeasonID(value string) (string, int, bool) {
	if !strings.HasPrefix(value, "season-") {
		return "", 0, false
	}
	value = strings.TrimPrefix(value, "season-")
	index := strings.LastIndexByte(value, '-')
	if index < 1 {
		return "", 0, false
	}
	number, err := strconv.Atoi(value[index+1:])
	return value[:index], number, err == nil && number >= 0
}

func (a *App) seriesParents(user User) string {
	if user.API || a.versionGrouping() == "id" {
		return "SELECT id FROM items WHERE id=? AND kind='Series'"
	}
	group := a.versionGrouping()
	return "SELECT id FROM items WHERE kind='Series' AND " + group + "=(SELECT " + group + " FROM items WHERE id=? AND kind='Series')"
}

// A season is part of a show, regardless of whether its files use a season folder.
func (a *App) seriesEpisodesWhere(user User, seriesID string) (string, []any) {
	parents := a.seriesParents(user)
	return "kind='Episode' AND (parent IN (" + parents + ") OR parent IN (SELECT id FROM items WHERE kind='Season' AND parent IN (" + parents + ")))", []any{seriesID, seriesID}
}

func (a *App) seasonEpisodesWhere(user User, seriesID string, number int) (string, []any) {
	if user.API {
		return "kind='Episode' AND parent IN (" + a.seriesParents(user) + ") AND season=?", []any{seriesID, number}
	}
	where, args := a.seriesEpisodesWhere(user, seriesID)
	return where + " AND season=?", append(args, number)
}

func (a *App) seasonEpisodeCounts(r *http.Request, user User, seriesID string, number int) (episodeCounts, error) {
	where, args := a.seasonEpisodesWhere(user, seriesID, number)
	if !user.API {
		where, args = a.mergeWhere(where, args)
	}
	var count episodeCounts
	err := a.mediaReader(r).QueryRow("SELECT count(*),count(*) FILTER (WHERE COALESCE(d.played,0)=0) FROM items LEFT JOIN userdata d ON d.item=items.id AND d.user_id=? WHERE "+where, append([]any{user.ID}, args...)...).Scan(&count.total, &count.unplayed)
	return count, err
}

func (a *App) viewerSeason(r *http.Request, user User, series Item, number int) Item {
	fallback := flatSeasonItem(series, number)
	if user.API {
		return fallback
	}
	key := flatSeasonID(series.ID, number)
	batch := catalogBatchFrom(r)
	if batch != nil {
		if season, ok := batch.seasons[key]; ok {
			return season
		}
	}
	season, err := readItem(a.mediaReader(r).QueryRow("SELECT "+cols+" FROM items WHERE kind='Season' AND parent IN ("+a.seriesParents(user)+") AND season=? ORDER BY id LIMIT 1", series.ID, number))
	if err != nil {
		season = fallback
	}
	if batch != nil {
		if batch.seasons == nil {
			batch.seasons = map[string]Item{}
		}
		batch.seasons[key] = season
	}
	return season
}

func (a *App) decorateEpisodeSeason(item Item, r *http.Request, user User, dto M) {
	if item.Kind != "Episode" || user.API {
		return
	}
	lookup := func(id string) Item {
		if batch := catalogBatchFrom(r); batch != nil {
			if parent, ok := batch.items[id]; ok {
				return parent
			}
		}
		parent, err := a.itemForUser(r, id)
		if batch := catalogBatchFrom(r); batch != nil && err == nil {
			batch.items[id] = parent
		}
		return parent
	}
	series := lookup(item.Parent)
	if series.Kind == "Season" {
		series = lookup(series.Parent)
	}
	if series.Kind != "Series" {
		return
	}
	season := a.viewerSeason(r, user, series, item.Season)
	if season.Parent != series.ID {
		series = lookup(season.Parent)
	}
	dto["ParentId"], dto["SeasonId"], dto["SeasonName"] = season.ID, season.ID, season.Name
	dto["SeriesId"], dto["SeriesName"] = series.ID, series.Name
}

func flatSeasonItem(series Item, number int) Item {
	name := fmt.Sprintf("第 %d 季", number)
	if number == 0 {
		name = "特别篇"
	}
	return Item{ID: flatSeasonID(series.ID, number), Lib: series.Lib, Parent: series.ID, Name: name, Kind: "Season", Year: series.Year, Season: number, Episode: number, AddedAt: series.AddedAt}
}

func (a *App) flatSeasonDTO(item Item, r *http.Request, user User, total, unplayed int) M {
	return M{"Id": item.ID, "ServerId": a.serverID, "ParentId": item.Parent, "SeriesId": item.Parent,
		"Name": item.Name, "SortName": item.Name, "Type": "Season", "IsFolder": true, "MediaType": "Video",
		"IndexNumber": item.Season, "ParentIndexNumber": item.Season, "ProductionYear": item.Year,
		"ImageTags": M{}, "BackdropImageTags": []string{}, "ChildCount": total, "RecursiveItemCount": total,
		"UserData": M{"Key": item.ID, "Played": total > 0 && unplayed == 0, "UnplayedItemCount": unplayed,
			"PlaybackPositionTicks": 0, "IsFavorite": false}}
}

// Flat show directories still have seasons, inferred from each episode's metadata.
func (a *App) showSeasons(w http.ResponseWriter, r *http.Request, user User, seriesID string) {
	series, err := a.itemForUser(r, seriesID)
	if err != nil || series.Kind != "Series" {
		fail(w, 404, "剧集不存在")
		return
	}
	reader := a.mediaReader(r)
	parents := a.seriesParents(user)
	where := "parent IN (" + parents + ") AND kind='Season'"
	args := []any{series.ID}
	if !user.API {
		where, args = a.mergeWhere(where, args)
	}
	rows, err := reader.Query("SELECT "+cols+" FROM items WHERE "+where+" ORDER BY season,id", args...)
	if err != nil {
		fail(w, 500, "读取季失败")
		return
	}
	seasons := []Item{}
	seen := map[int]bool{}
	for rows.Next() {
		item, e := readItem(rows)
		if e != nil {
			err = e
			break
		}
		if user.API || !seen[item.Season] {
			seasons = append(seasons, item)
			seen[item.Season] = true
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		fail(w, 500, "读取季失败")
		return
	}
	out := a.listDTOs(seasons, r, user)
	where, args = a.seriesEpisodesWhere(user, series.ID)
	if user.API {
		where, args = "kind='Episode' AND parent IN ("+parents+")", []any{series.ID}
	} else {
		where, args = a.mergeWhere(where, args)
	}
	rows, err = reader.Query("SELECT season,count(*),count(*) FILTER (WHERE COALESCE(d.played,0)=0) FROM items LEFT JOIN userdata d ON d.item=items.id AND d.user_id=? WHERE "+where+" GROUP BY season ORDER BY season", append([]any{user.ID}, args...)...)
	if err != nil {
		fail(w, 500, "读取单集季号失败")
		return
	}
	for rows.Next() {
		var number, total, unplayed int
		if err = rows.Scan(&number, &total, &unplayed); err != nil {
			break
		}
		if number >= 0 {
			if !user.API && seen[number] {
				for _, dto := range out {
					if dto["IndexNumber"] == number {
						dto["ChildCount"], dto["RecursiveItemCount"] = total, total
						data := dto["UserData"].(M)
						data["UnplayedItemCount"], data["Played"] = unplayed, total > 0 && unplayed == 0
						break
					}
				}
				continue
			}
			item := flatSeasonItem(series, number)
			dto := a.flatSeasonDTO(item, r, user, total, unplayed)
			for _, real := range seasons {
				if real.Season == number {
					dto["Name"] = item.Name + " · 剧集根目录"
					break
				}
			}
			out = append(out, dto)
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		fail(w, 500, "读取单集季号失败")
		return
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i]["IndexNumber"].(int) < out[j]["IndexNumber"].(int) })
	if strings.EqualFold(q(r, "SortOrder"), "Descending") {
		for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
			out[i], out[j] = out[j], out[i]
		}
	}
	count := len(out)
	start, _ := strconv.Atoi(q(r, "StartIndex"))
	start = max(0, min(start, count))
	end := count
	if limit, _ := strconv.Atoi(q(r, "Limit")); limit > 0 {
		end = start + min(limit, count-start)
	}
	respond(w, M{"Items": out[start:end], "TotalRecordCount": count, "StartIndex": start, "HasMore": end < count})
}
