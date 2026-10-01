package main

import (
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestSearchTextAndPartialInitials(t *testing.T) {
	a := testApp(t)
	if _, e := a.db.Exec("INSERT INTO libraries(id,name,path,kind) VALUES('search-lib','Search','/search','movies')"); e != nil {
		t.Fatal(e)
	}
	for _, name := range []string{"流浪地球", "流浪地球2", "流浪猫", "100%真实", "普通电影"} {
		if _, e := a.db.Exec("INSERT INTO items(id,lib,parent,name,kind,path,seen) VALUES(?,'search-lib','search-lib',?,'Movie',?,'test')", id(), name, "/search/"+name); e != nil {
			t.Fatal(e)
		}
	}
	for _, enabled := range []bool{true, false} {
		value := "false"
		if enabled {
			value = "true"
		}
		if _, e := a.db.Exec("INSERT INTO settings(k,v) VALUES('search_by_initials',?) ON CONFLICT(k) DO UPDATE SET v=excluded.v", value); e != nil {
			t.Fatal(e)
		}
		for _, tc := range []struct {
			key, term string
			on, off   int
		}{
			{"SearchTerm", "流浪", 3, 3}, {"SearchTerm", "流浪地球", 2, 2}, {"SearchTerm", "ll", 3, 0}, {"SearchTerm", "LL", 3, 0}, {"SearchTerm", " lldq ", 2, 0}, {"SearchTerm", "100%", 1, 1}, {"SearchTerm", "_", 0, 0}, {"NameStartsWith", "流浪", 3, 3}, {"NameStartsWith", "ll", 3, 0},
		} {
			w := httptest.NewRecorder()
			a.items(w, httptest.NewRequest("GET", "/Items?IncludeItemTypes=Movie&"+url.Values{tc.key: {tc.term}}.Encode(), nil), User{}, false)
			var result struct {
				Items            []M
				TotalRecordCount int
			}
			e := json.Unmarshal(w.Body.Bytes(), &result)
			want := tc.off
			if enabled {
				want = tc.on
			}
			if e != nil || w.Code != 200 || result.TotalRecordCount != want || len(result.Items) != want {
				t.Fatalf("enabled=%v %s=%q: HTTP %d %s", enabled, tc.key, tc.term, w.Code, w.Body.String())
			}
		}
	}
}
