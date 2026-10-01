package main

import (
	"fmt"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func TestCursorPagination(t *testing.T) {
	a := testApp(t)
	if _, e := a.db.Exec("INSERT INTO libraries(id,name,path,kind) VALUES('p','p','/p','movies')"); e != nil {
		t.Fatal(e)
	}
	if _, e := a.db.Exec(`INSERT INTO items(id,lib,parent,name,kind,path,year,mtime,season,episode,seen) SELECT lpad(n::text,5,'0'),'p','p','name-'||(n/7)::text,'Movie','/p/'||n,2000+n%5,n%9,n%3,n%7,'s' FROM generate_series(1,401)n`); e != nil {
		t.Fatal(e)
	}
	for _, sort := range []string{"SortName", "ProductionYear", "DateCreated", "ParentIndexNumber,IndexNumber"} {
		for _, dir := range []string{"Ascending", "Descending"} {
			path := "/Items?ParentId=p&Limit=17&SortBy=" + sort + "&SortOrder=" + dir
			seen := map[string]bool{}
			cursor := ""
			pages := 0
			for {
				r := httptest.NewRequest("GET", path+"&Cursor="+url.QueryEscape(cursor), nil)
				rows, p, next, more, e := a.pageItems(r, User{API: true}, "lib=?", []any{"p"}, browseOrder(r), 17)
				if e != nil {
					t.Fatal(e)
				}
				if p.Count != 401 {
					t.Fatal(p.Count)
				}
				for _, x := range rows {
					if seen[x.ID] {
						t.Fatalf("duplicate %s", x.ID)
					}
					seen[x.ID] = true
				}
				pages++
				if pages > 30 {
					t.Fatal("unbounded pages")
				}
				if !more {
					break
				}
				cursor = next
			}
			if len(seen) != 401 {
				t.Fatal(sort, dir, len(seen))
			}
		}
	}
	r := httptest.NewRequest("GET", "/Items?StartIndex=400000", nil)
	if p, e := a.pageStart(r, "unknown"); e != nil || p.Position != 400000 {
		t.Fatal("standard StartIndex rejected")
	}
	p := pageToken{Query: "q", Values: []string{"x"}, Expires: time.Now().Add(time.Minute).Unix()}
	s := a.encodePage(p)
	if _, e := a.decodePage(s, "other"); e == nil {
		t.Fatal("cross-query cursor accepted")
	}
	if _, e := a.decodePage(s+"x", "q"); e == nil {
		t.Fatal("tampered cursor accepted")
	}
}
func TestPostgresScale400K(t *testing.T) {
	if os.Getenv("SCALE_TEST") != "1" {
		t.Skip("SCALE_TEST=1 enables 400k catalog test")
	}
	a := testApp(t)
	a.db.Exec("INSERT INTO libraries(id,name,path,kind) VALUES('scale','scale','/scale','tvshows')")
	start := time.Now()
	_, e := a.db.Exec(`INSERT INTO items(id,lib,parent,name,kind,path,year,mtime,season,episode,seen) SELECT lpad(n::text,8,'0'),'scale','season-'||(n/20)::text,'Episode '||lpad(n::text,8,'0'),'Episode','/scale/'||n||'.strm',2000+n%25,1700000000000000000+n,n/20,n%20,'s' FROM generate_series(1,400000)n`)
	if e != nil {
		t.Fatal(e)
	}
	a.db.Exec("ANALYZE items")
	t.Logf("400000 rows inserted in %s", time.Since(start))
	for _, query := range []string{
		`SELECT id,name FROM items WHERE lib='scale' AND kind='Episode' AND (name,id)>('Episode 00399000','00399000') ORDER BY name,id LIMIT 61`,
		`SELECT id,mtime FROM items WHERE lib='scale' AND kind='Episode' AND (mtime,id)<(1700000000000399000,'00399000') ORDER BY mtime DESC,id DESC LIMIT 61`,
		`SELECT id FROM items WHERE parent='season-19950' AND (season,episode,id)>(19950,0,'00399000') ORDER BY season,episode,id LIMIT 61`,
	} {
		rows, e := a.db.Query("EXPLAIN (ANALYZE,BUFFERS,FORMAT TEXT) " + query)
		if e != nil {
			t.Fatal(e)
		}
		lines := []string{}
		for rows.Next() {
			var line string
			rows.Scan(&line)
			lines = append(lines, line)
		}
		rows.Close()
		plan := strings.Join(lines, "\n")
		t.Log(plan)
		if !strings.Contains(plan, "Index") || strings.Contains(plan, "Seq Scan") {
			t.Fatal("unindexed deep page", plan)
		}
	}
	r := httptest.NewRequest("GET", "/Items?ParentId=scale&Limit=60&EnableTotalRecordCount=false", nil)
	key := a.pageSignature(r, "lib=? AND kind=?", "name ASC,id ASC", []any{"scale", "Episode"})
	cursor := a.encodePage(pageToken{Query: key, Values: []string{"Episode 00399000", "00399000"}, Position: 399000, Count: -1, Expires: time.Now().Add(time.Minute).Unix()})
	setQuery(r, "Cursor", cursor)
	start = time.Now()
	items, _, _, _, e := a.pageItems(r, User{API: true}, "lib=? AND kind=?", []any{"scale", "Episode"}, "name ASC,id ASC", 60)
	if e != nil || len(items) != 60 || items[0].ID != "00399001" {
		t.Fatal(fmt.Sprint(e), len(items))
	}
	t.Logf("deep page 399000: %s, %d rows", time.Since(start), len(items))
}

func TestBackupConnectionEnvironment(t *testing.T) {
	env, e := postgresBackupEnv("postgres://emby:secret@postgres:5432/emby?sslmode=disable")
	if e != nil {
		t.Fatal(e)
	}
	for _, want := range []string{"PGUSER=emby", "PGPASSWORD=secret", "PGDATABASE=emby", "PGHOST=postgres", "PGPORT=5432", "PGSSLMODE=disable"} {
		found := false
		for _, v := range env {
			if v == want {
				found = true
			}
		}
		if !found {
			t.Fatal("missing", strings.Split(want, "=")[0])
		}
	}
}

func TestMovieListOmitsEpisodeNumbers(t *testing.T) {
	a := &App{}
	for _, kind := range []string{"Movie", "Episode"} {
		m := a.listDTO(Item{Kind: kind}, nil, User{})
		for _, key := range []string{"IndexNumber", "ParentIndexNumber"} {
			_, exists := m[key]
			if exists != (kind == "Episode") {
				t.Fatalf("%s: incorrect %s presence", kind, key)
			}
		}
	}
}
