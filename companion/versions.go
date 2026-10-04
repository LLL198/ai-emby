package main

import (
	"net/http"
	"path/filepath"
	"strings"
)

// Stable identity, independent of video encoding. Unknown episodes stay separate.
const versionExpr = "media_version_key_v2(kind,path,name,year,season,episode,id)"

func (a *App) versionsSchema() error {
	_, e := a.db.Exec(`CREATE OR REPLACE FUNCTION media_version_key_v2(k text,p text,n text,y bigint,s bigint,ep bigint,i text) RETURNS text LANGUAGE sql IMMUTABLE PARALLEL SAFE AS '
 SELECT CASE WHEN k NOT IN (''Movie'',''Episode'',''Series'',''Season'') THEN ''id:''||i
 WHEN k IN (''Episode'',''Season'') AND ((k=''Episode'' AND ep<=0) OR substring(p from ''[{]tmdb(?:id)?-([0-9]+)[}]'') IS NULL) THEN ''id:''||i
 ELSE k||'':''||COALESCE(''tmdb:''||substring(p from ''[{]tmdb(?:id)?-([0-9]+)[}]''),CASE WHEN y>0 THEN ''name:''||lower(trim(n))||'':''||y::text ELSE ''id:''||i END)||CASE WHEN k=''Episode'' THEN '':''||s::text||'':''||ep::text WHEN k=''Season'' THEN '':''||s::text ELSE '''' END END';
 CREATE INDEX IF NOT EXISTS items_version_key_v2 ON items(media_version_key_v2(kind,path,name,year,season,episode,id));`)
	return e
}
func (a *App) versionGrouping() string {
	return a.versionGroupingFor("")
}

func (a *App) versionGroupingFor(alias string) string {
	column := func(name string) string {
		if alias != "" {
			return alias + "." + name
		}
		return name
	}
	expr := "media_version_key_v2(" + strings.Join([]string{column("kind"), column("path"), column("name"), column("year"), column("season"), column("episode"), column("id")}, ",") + ")"
	if a.defaultOn("merge_versions_libraries") {
		return expr
	}
	if a.defaultOn("merge_versions_folder") {
		return expr + "||':'||" + column("lib") + "||':'||CASE WHEN " + column("kind") + "='Series' THEN " + column("path") + " ELSE regexp_replace(" + column("path") + ",'/[^/]*$','') END"
	}
	return column("id")
}
func (a *App) mergeWhere(where string, args []any) (string, []any) {
	group := a.versionGrouping()
	if group == "id" {
		return where, args
	}
	// Choose representatives inside the filtered set, preserving library/search scope.
	return "id IN (SELECT DISTINCT ON (" + group + ") id FROM items WHERE " + where + " ORDER BY " + group + ",id)", args
}
func (a *App) versions(x Item) []Item {
	if x.URL == "" {
		return []Item{x}
	}
	group := a.versionGrouping()
	rows, e := a.db.Query("SELECT "+cols+" FROM items WHERE "+group+"=(SELECT "+group+" FROM items WHERE id=?) AND url<>'' ORDER BY id", x.ID)
	if e != nil {
		return []Item{x}
	}
	defer rows.Close()
	out := []Item{x}
	for rows.Next() {
		v, e := readItem(rows)
		if e != nil {
			return []Item{x}
		}
		if v.ID != x.ID {
			out = append(out, v)
		}
	}
	if rows.Err() != nil {
		return []Item{x}
	}
	return out
}
func (a *App) versionSources(x Item, r *http.Request, u User, display bool) []M {
	out := []M{}
	versions := []Item{x}
	if !u.API {
		group := a.versionGrouping()
		rows, err := a.mediaReader(r).Query("SELECT "+cols+" FROM items WHERE "+group+"=(SELECT "+group+" FROM items WHERE id=?) AND url<>'' ORDER BY id", x.ID)
		if err == nil {
			loaded := []Item{x}
			for rows.Next() {
				item, e := readItem(rows)
				if e != nil {
					err = e
					break
				}
				if item.ID != x.ID {
					loaded = append(loaded, item)
				}
			}
			if err == nil {
				err = rows.Err()
			}
			rows.Close()
			if err == nil {
				versions = loaded
			}
		}
	}
	for _, v := range versions {
		m := a.viewerSource(v, r, u)
		m["Name"] = filepath.Base(v.Path)
		m["ItemId"] = v.ID
		m["LibraryId"] = v.Lib
		out = append(out, m)
	}
	return out
}
