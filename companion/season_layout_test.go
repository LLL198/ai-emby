package main

import (
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func seasonLayoutFixture(t *testing.T) (*App, string) {
	t.Helper()
	a := testApp(t)
	var uid string
	if err := a.db.QueryRow("SELECT id FROM users LIMIT 1").Scan(&uid); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec("INSERT INTO tokens VALUES(?,?,?,?)", digest("browse-test"), uid, "season-device", time.Now().Unix()+600); err != nil {
		t.Fatal(err)
	}
	for _, lib := range []string{"a", "b"} {
		if _, err := a.db.Exec("INSERT INTO libraries(id,name,path,kind) VALUES(?,?,?,'tvshows')", lib, lib, "/media/"+lib); err != nil {
			t.Fatal(err)
		}
	}
	for _, x := range []struct {
		id, lib, parent, kind string
		season, episode       int
	}{
		{"show-a", "a", "a", "Series", 0, 0},
		{"show-b", "b", "b", "Series", 0, 0},
		{"real-season", "a", "show-a", "Season", 1, 1},
		{"a1", "a", "real-season", "Episode", 1, 1},
		{"a2", "a", "real-season", "Episode", 1, 2},
		{"b1", "b", "show-b", "Episode", 1, 1},
		{"b3", "b", "show-b", "Episode", 1, 3},
		{"b4", "b", "show-b", "Episode", 2, 1},
		{"b0", "b", "show-b", "Episode", 0, 1},
	} {
		path := filepath.Join("/media", x.lib, "Show (2026) {tmdb-294486}")
		name, source := "Show", ""
		if x.kind == "Season" || x.parent == "real-season" {
			path = filepath.Join(path, "Season 01")
		}
		if x.kind == "Episode" {
			name = fmt.Sprintf("Episode %d", x.episode)
			path = filepath.Join(path, x.id+".strm")
			source = "https://example.invalid/" + x.id
		}
		if _, err := a.db.Exec("INSERT INTO items(id,lib,parent,name,kind,path,url,year,season,episode,seen) VALUES(?,?,?,?,?,?,?,2026,?,?,'g')", x.id, x.lib, x.parent, name, x.kind, path, source, x.season, x.episode); err != nil {
			t.Fatal(err)
		}
	}
	return a, uid
}

func TestMixedSeasonLayoutsShareOneSeason(t *testing.T) {
	a, uid := seasonLayoutFixture(t)
	if _, err := a.db.Exec("INSERT INTO userdata(user_id,item,played,position) VALUES(?,'a1',1,123)", uid); err != nil {
		t.Fatal(err)
	}
	listing := decodeM(t, browseGet(a, "/Shows/show-a/Seasons", true))
	seasons := listing["Items"].([]any)
	if listing["TotalRecordCount"] != float64(3) {
		t.Fatalf("same season listed twice: %v", listing)
	}
	for _, raw := range seasons {
		season := raw.(map[string]any)
		if season["IndexNumber"] == float64(1) {
			if season["Id"] != "real-season" || season["RecursiveItemCount"] != float64(3) || season["ChildCount"] != float64(3) {
				t.Fatalf("real season should contain three logical episodes: %v", season)
			}
			if season["UserData"].(map[string]any)["UnplayedItemCount"] != float64(2) {
				t.Fatal("merged season play state differs from episode list", season)
			}
		}
	}
	for _, endpoint := range []string{
		"/Shows/show-a/Episodes?SeasonId=real-season",
		"/Shows/show-a/Episodes?SeasonId=season-show-a-1",
		"/Items?ParentId=real-season",
		"/Items?ParentId=season-show-a-1",
	} {
		page := decodeM(t, browseGet(a, endpoint+"&Fields=MediaSources,Overview", true))
		if page["TotalRecordCount"] != float64(3) || len(page["Items"].([]any)) != 3 {
			t.Fatalf("%s lost episodes: %v", endpoint, page)
		}
		for _, raw := range page["Items"].([]any) {
			episode := raw.(map[string]any)
			if episode["SeasonId"] != "real-season" || episode["ParentId"] != "real-season" {
				t.Fatalf("episode points to a second season: %v", episode)
			}
			if episode["IndexNumber"] == float64(1) && len(episode["MediaSources"].([]any)) != 2 {
				t.Fatal("alternate playback version lost")
			}
		}
	}
	page := decodeM(t, browseGet(a, "/Shows/show-a/Episodes?SeasonId=real-season&Limit=1&StartIndex=1", true))
	if page["TotalRecordCount"] != float64(3) || len(page["Items"].([]any)) != 1 || page["Items"].([]any)[0].(map[string]any)["IndexNumber"] != float64(2) {
		t.Fatal("merged season pagination broken", page)
	}
	for _, endpoint := range []string{"/Items/b3", "/Items?Ids=b3&Fields=MediaSources,Overview"} {
		detail := decodeM(t, browseGet(a, endpoint, true))
		if items, ok := detail["Items"].([]any); ok {
			detail = M(items[0].(map[string]any))
		}
		if detail["SeasonId"] != "real-season" || detail["ParentId"] != "real-season" {
			t.Fatal("single episode season differs from list", detail)
		}
	}
	for _, endpoint := range []string{"/Items/season-show-a-1", "/Items/real-season"} {
		alias := decodeM(t, browseGet(a, endpoint, true))
		if alias["RecursiveItemCount"] != float64(3) || alias["ChildCount"] != float64(3) {
			t.Fatal("season details lost episodes", alias)
		}
	}
	var played, position int
	if err := a.db.QueryRow("SELECT played,position FROM userdata WHERE user_id=? AND item='a1'", uid).Scan(&played, &position); err != nil || played != 1 || position != 123 {
		t.Fatal("season regrouping changed viewing history", err)
	}
}

func TestMixedSeasonLayoutsInOneShowWithoutVersionMerging(t *testing.T) {
	a, _ := seasonLayoutFixture(t)
	if _, err := a.db.Exec("UPDATE items SET parent='show-a',lib='a' WHERE id='b3'"); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"merge_versions_libraries", "merge_versions_folder"} {
		if _, err := a.db.Exec("INSERT INTO settings(k,v) VALUES(?,'false') ON CONFLICT(k) DO UPDATE SET v=excluded.v", key); err != nil {
			t.Fatal(err)
		}
	}
	seasons := decodeM(t, browseGet(a, "/Shows/show-a/Seasons", true))
	if seasons["TotalRecordCount"] != float64(1) {
		t.Fatal("flat files and a folder in one show still created duplicate seasons", seasons)
	}
	for _, endpoint := range []string{"/Items?ParentId=real-season", "/Items?ParentId=season-show-a-1"} {
		page := decodeM(t, browseGet(a, endpoint, true))
		if page["TotalRecordCount"] != float64(3) {
			t.Fatal("season split without version merging", page)
		}
		for _, raw := range page["Items"].([]any) {
			if raw.(map[string]any)["SeasonId"] != "real-season" {
				t.Fatal("plain episode list uses a second season", raw)
			}
		}
	}
}

func TestMixedSeasonLayoutsRespectVisibilityAndGrouping(t *testing.T) {
	a, uid := seasonLayoutFixture(t)
	if _, err := a.db.Exec("UPDATE users SET admin=0 WHERE id=?", uid); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec("UPDATE libraries SET hidden=1 WHERE id='a'"); err != nil {
		t.Fatal(err)
	}
	seasons := decodeM(t, browseGet(a, "/Shows/show-b/Seasons", true))
	if seasons["TotalRecordCount"] != float64(3) {
		t.Fatal(seasons)
	}
	page := decodeM(t, browseGet(a, "/Items?ParentId=season-show-b-1&Fields=MediaSources,Overview", true))
	if page["TotalRecordCount"] != float64(2) {
		t.Fatal("hidden season episodes leaked", page)
	}
	for _, raw := range page["Items"].([]any) {
		ep := raw.(map[string]any)
		if ep["SeasonId"] != "season-show-b-1" || len(ep["MediaSources"].([]any)) != 1 {
			t.Fatal("hidden season identity or source leaked", ep)
		}
	}
	if _, err := a.db.Exec("UPDATE libraries SET hidden=0"); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"merge_versions_libraries", "merge_versions_folder"} {
		if _, err := a.db.Exec("INSERT INTO settings(k,v) VALUES(?,'false') ON CONFLICT(k) DO UPDATE SET v=excluded.v", key); err != nil {
			t.Fatal(err)
		}
	}
	page = decodeM(t, browseGet(a, "/Items?ParentId=real-season&Fields=MediaSources,Overview", true))
	if page["TotalRecordCount"] != float64(2) {
		t.Fatal("separate shows were combined with grouping disabled", page)
	}
}

func TestMixedSeasonLayoutsKeepServiceAPISourceScope(t *testing.T) {
	a, _ := seasonLayoutFixture(t)
	w := httptest.NewRecorder()
	a.items(w, httptest.NewRequest("GET", "/Items?ParentId=real-season", nil), User{API: true}, false)
	page := decodeM(t, w)
	if page["TotalRecordCount"] != float64(2) {
		t.Fatal("service API physical season changed", page)
	}
}
