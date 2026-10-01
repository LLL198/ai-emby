package main

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRequestFixCatalog(t *testing.T) {
	a := testApp(t)
	_, e := a.db.Exec("INSERT INTO libraries(id,name,path,kind) VALUES('lib','lib','/media/lib','tvshows')")
	if e != nil {
		t.Fatal(e)
	}
	for _, v := range []struct {
		id, parent, name, kind, path string
		season, episode              int
	}{
		{"s1", "lib", "流浪地球", "Series", "/media/a/{tmdb-7}", 0, 0},
		{"s2", "lib", "流浪地球", "Series", "/media/b/{tmdbid-7}", 0, 0},
		{"z1", "s1", "Season 1", "Season", "/media/a/{tmdb-7}/Season 1", 1, 0},
		{"z2", "s2", "Season 1", "Season", "/media/b/{tmdbid-7}/Season 1", 1, 0},
		{"e1", "z1", "First", "Episode", "/media/a/{tmdb-7}/Season 1/1.strm", 1, 1},
		{"e2", "z2", "First", "Episode", "/media/b/{tmdbid-7}/Season 1/1.strm", 1, 1},
		{"e3", "z2", "Second", "Episode", "/media/b/{tmdbid-7}/Season 1/2.strm", 1, 2},
	} {
		_, e = a.db.Exec("INSERT INTO items(id,lib,parent,name,kind,path,year,season,episode,seen) VALUES(?,'lib',?,?,?,?,2020,?,?,'test')", v.id, v.parent, v.name, v.kind, v.path, v.season, v.episode)
		if e != nil {
			t.Fatal(e)
		}
	}
	for _, q := range []struct {
		query string
		count int
	}{
		{"IncludeItemTypes=Series&SearchTerm=lldq", 1},
		{"IncludeItemTypes=Series&SearchTerm=ll", 1},
		{"IncludeItemTypes=Series&SearchTerm=LL", 1},
		{"IncludeItemTypes=Series&SearchTerm=%20ll%20", 1},
		{"IncludeItemTypes=Series&SearchTerm=流浪", 1},
		{"IncludeItemTypes=Series&SearchTerm=流浪地球", 1},
		{"IncludeItemTypes=Series&NameStartsWith=流浪", 1},
		{"IncludeItemTypes=Series&NameStartsWith=ll", 1},
		{"ParentId=s1", 1}, {"ParentId=z1", 2},
		{"ParentId=s1&Recursive=true&IncludeItemTypes=Episode", 2},
		{"ParentId=z1&Limit=1&StartIndex=1", 2},
	} {
		w := httptest.NewRecorder()
		a.items(w, httptest.NewRequest("GET", "/Items?"+q.query, nil), User{}, false)
		var b struct {
			Items            []M
			TotalRecordCount int
		}
		json.Unmarshal(w.Body.Bytes(), &b)
		if w.Code != 200 || b.TotalRecordCount != q.count || len(b.Items) == 0 {
			t.Fatalf("%s: %d %s", q.query, w.Code, w.Body.String())
		}
	}
	a.db.Exec("INSERT INTO settings(k,v) VALUES('search_by_initials','false')")
	w := httptest.NewRecorder()
	a.items(w, httptest.NewRequest("GET", "/Items?IncludeItemTypes=Series&SearchTerm=lldq", nil), User{}, false)
	var b struct{ Items []M }
	json.Unmarshal(w.Body.Bytes(), &b)
	if len(b.Items) != 0 {
		t.Fatal("initials toggle ignored")
	}
	w = httptest.NewRecorder()
	a.items(w, httptest.NewRequest("GET", "/Items?IncludeItemTypes=Series&SearchTerm=流浪", nil), User{}, false)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "流浪地球") {
		t.Fatal("Chinese search disabled", w.Body.String())
	}
}
func TestRequestFixActorsAndConcurrency(t *testing.T) {
	a := testApp(t)
	a.db.Exec("INSERT INTO libraries(id,name,path,kind) VALUES('l','l','/l','movies')")
	a.db.Exec("INSERT INTO items(id,lib,parent,name,kind,path,seen) VALUES('i','l','l','i','Movie','/i','s')")
	a.db.Exec("INSERT INTO item_people VALUES('i','person-empty','Empty','','Actor',''),('i','person-photo','Photo','','Actor','https://image.tmdb.org/t/p/w185/photo.jpg')")
	w := httptest.NewRecorder()
	a.persons(w, httptest.NewRequest("GET", "/Persons", nil))
	if strings.Contains(w.Body.String(), "Empty") || !strings.Contains(w.Body.String(), "Primary") {
		t.Fatal(w.Body.String())
	}
	original := a.probeSettings()
	w = httptest.NewRecorder()
	a.mediaSettings(w, httptest.NewRequest("PUT", "/admin/media-info", strings.NewReader(`{"Concurrency":0}`)))
	if w.Code != 400 || a.probeSettings() != original {
		t.Fatal("zero saved", w.Code)
	}
}
