package main

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

func (a *App) featureAllowedLibraries(user User) []string {
	rows, err := a.db.Query("SELECT id FROM libraries")
	if err != nil {
		return nil
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	allowed := []string{}
	for _, id := range ids {
		if a.featureLibraryAllowed(user, id) {
			allowed = append(allowed, id)
		}
	}
	return allowed
}
func featurePlaceholders(n int) string {
	if n == 0 {
		return "NULL"
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}
func (a *App) featureCatalogQuery(r *http.Request, user User) (string, []any) {
	ids := a.featureAllowedLibraries(user)
	args := []any{}
	for _, id := range ids {
		args = append(args, id)
	}
	where := "i.kind IN ('Movie','Series') AND i.lib IN (" + featurePlaceholders(len(ids)) + ")"
	q := r.URL.Query()
	if lib := q.Get("Library"); lib != "" {
		where += " AND i.lib=?"
		args = append(args, lib)
	}
	if kind := q.Get("Type"); kind == "Movie" || kind == "Series" {
		where += " AND i.kind=?"
		args = append(args, kind)
	}
	if search := q.Get("Search"); search != "" {
		where += " AND i.name ILIKE ?"
		args = append(args, "%"+strings.ReplaceAll(strings.ReplaceAll(search, "%", "\\%"), "_", "\\_")+"%")
	}
	if year, err := strconv.Atoi(q.Get("Year")); err == nil && year > 0 {
		where += " AND i.year=?"
		args = append(args, year)
	}
	for _, f := range []struct{ param, key string }{{"Genre", "Genres"}, {"Country", "Countries"}, {"Tag", "Tags"}} {
		for _, value := range q[f.param] {
			if value != "" {
				where += " AND COALESCE(f.data::jsonb,m.data::jsonb,'{}'::jsonb)->'" + f.key + "' @> ?::jsonb"
				args = append(args, featureJSON([]string{value}))
			}
		}
	}
	return where, args
}
func (a *App) featureCatalogAPI(w http.ResponseWriter, r *http.Request, user User) {
	if !featureMethod(w, r, http.MethodGet) {
		return
	}
	where, args := a.featureCatalogQuery(r, user)
	join := " FROM items i LEFT JOIN feature_metadata f ON f.item=i.id LEFT JOIN item_metadata m ON m.item=i.id WHERE " + where
	var count int
	if err := a.db.QueryRow("SELECT count(*)"+join, args...).Scan(&count); err != nil {
		featureError(w, err)
		return
	}
	order := "i.added_at"
	switch r.URL.Query().Get("Sort") {
	case "name":
		order = "COALESCE(NULLIF(i.sort_name,''),i.name)"
	case "year":
		order = "i.year"
	case "rating":
		order = "COALESCE(NULLIF(COALESCE(f.data::jsonb,m.data::jsonb)->>'Rating','')::double precision,0)"
	case "premiere":
		order = "i.premiere_date"
	}
	direction := " DESC"
	if r.URL.Query().Get("Order") == "Ascending" {
		direction = " ASC"
	}
	start, _ := strconv.Atoi(r.URL.Query().Get("Start"))
	start = max(0, start)
	limit := featureLimit(r, 48, 120)
	columns := strings.Split(cols, ",")
	for i := range columns {
		columns[i] = "i." + columns[i]
	}
	args = append(args, limit, start)
	rows, err := a.db.Query("SELECT "+strings.Join(columns, ",")+join+" ORDER BY "+order+direction+",i.id LIMIT ? OFFSET ?", args...)
	if err != nil {
		featureError(w, err)
		return
	}
	items := []Item{}
	for rows.Next() {
		x, e := readItem(rows)
		if e != nil {
			rows.Close()
			featureError(w, e)
			return
		}
		items = append(items, x)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		featureError(w, err)
		return
	}
	out := []M{}
	for _, x := range items {
		out = append(out, a.featureDTO(x, user))
	}
	respond(w, M{"Items": out, "TotalRecordCount": count, "StartIndex": start})
}
func (a *App) featureFacetsAPI(w http.ResponseWriter, r *http.Request, user User) {
	if !featureMethod(w, r, http.MethodGet) {
		return
	}
	ids := a.featureAllowedLibraries(user)
	args := []any{}
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := a.db.Query("SELECT i.year,COALESCE(f.data,m.data,'{}') FROM items i LEFT JOIN feature_metadata f ON f.item=i.id LEFT JOIN item_metadata m ON m.item=i.id WHERE i.kind IN ('Movie','Series') AND i.lib IN ("+featurePlaceholders(len(ids))+")", args...)
	if err != nil {
		featureError(w, err)
		return
	}
	defer rows.Close()
	years := map[int]bool{}
	facets := map[string]map[string]bool{"Genres": {}, "Countries": {}, "Tags": {}}
	for rows.Next() {
		var year int
		var raw string
		if rows.Scan(&year, &raw) != nil {
			continue
		}
		if year > 0 {
			years[year] = true
		}
		var values map[string]json.RawMessage
		if json.Unmarshal([]byte(raw), &values) != nil {
			continue
		}
		for name, set := range facets {
			var list []string
			_ = json.Unmarshal(values[name], &list)
			for _, v := range list {
				if v != "" {
					set[v] = true
				}
			}
		}
	}
	out := M{"Libraries": ids}
	list := []int{}
	for v := range years {
		list = append(list, v)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(list)))
	out["Years"] = list
	for name, set := range facets {
		list := []string{}
		for v := range set {
			list = append(list, v)
		}
		sort.Strings(list)
		out[name] = list
	}
	respond(w, out)
}
func (a *App) featureCollectionOwner(user User, id string, write bool) bool {
	var owner string
	var public int
	if a.db.QueryRow("SELECT owner,public FROM feature_collections WHERE id=?", id).Scan(&owner, &public) != nil {
		return false
	}
	return user.Admin || owner == user.ID || !write && public == 1
}
func (a *App) featureCollectionsAPI(w http.ResponseWriter, r *http.Request, user User) {
	if !featureMethod(w, r, http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete) {
		return
	}
	if r.Method == http.MethodGet {
		rows, err := a.db.Query("SELECT c.id,c.name,c.owner,c.public,c.created FROM feature_collections c WHERE c.owner=? OR c.public=1 OR ?=1 ORDER BY c.created DESC", user.ID, boolInt(user.Admin))
		if err != nil {
			featureError(w, err)
			return
		}
		items := []M{}
		for rows.Next() {
			var id, name, owner string
			var public, created int64
			if rows.Scan(&id, &name, &owner, &public, &created) == nil {
				items = append(items, M{"ID": id, "Name": name, "Owner": owner, "Public": public == 1, "Editable": owner == user.ID || user.Admin})
			}
		}
		rows.Close()
		for _, item := range items {
			var count int
			_ = a.db.QueryRow("SELECT count(*) FROM feature_collection_items WHERE collection=?", item["ID"]).Scan(&count)
			item["Count"] = count
		}
		respond(w, M{"Items": items})
		return
	}
	if user.API {
		fail(w, 403, "需要用户登录")
		return
	}
	var request struct {
		ID, Name string
		Public   bool
	}
	if !body(w, r, &request) {
		return
	}
	if r.Method != http.MethodPost && !a.featureCollectionOwner(user, request.ID, true) {
		fail(w, 404, "合集不存在")
		return
	}
	var err error
	if r.Method == http.MethodDelete {
		_, err = a.db.Exec("DELETE FROM feature_collections WHERE id=?", request.ID)
	} else {
		request.Name = strings.TrimSpace(request.Name)
		if request.Name == "" || len(request.Name) > 256 {
			fail(w, 400, "合集名称无效")
			return
		}
		if r.Method == http.MethodPost {
			request.ID = id()
			_, err = a.db.Exec("INSERT INTO feature_collections VALUES(?,?,?,?,?)", request.ID, request.Name, user.ID, boolInt(request.Public), featureNow())
		} else {
			_, err = a.db.Exec("UPDATE feature_collections SET name=?,public=? WHERE id=?", request.Name, boolInt(request.Public), request.ID)
		}
	}
	if err != nil {
		featureError(w, err)
		return
	}
	respond(w, M{"ID": request.ID, "ok": true})
}
func (a *App) featureCollectionItems(w http.ResponseWriter, r *http.Request, user User) {
	if !featureMethod(w, r, http.MethodGet, http.MethodPost, http.MethodDelete) {
		return
	}
	if r.Method == http.MethodGet {
		collection := r.URL.Query().Get("ID")
		if !a.featureCollectionOwner(user, collection, false) {
			fail(w, 404, "合集不存在")
			return
		}
		rows, err := a.db.Query("SELECT item FROM feature_collection_items WHERE collection=? ORDER BY added DESC LIMIT ?", collection, featureLimit(r, 100, 500))
		if err != nil {
			featureError(w, err)
			return
		}
		ids := []string{}
		for rows.Next() {
			var id string
			if rows.Scan(&id) == nil {
				ids = append(ids, id)
			}
		}
		rows.Close()
		items := []M{}
		for _, id := range ids {
			if x, e := a.featureAccessibleItem(user, id); e == nil {
				items = append(items, a.featureDTO(x, user))
			}
		}
		respond(w, M{"Items": items})
		return
	}
	if user.API {
		fail(w, 403, "需要用户登录")
		return
	}
	var request struct {
		ID    string
		Items []string
	}
	if !body(w, r, &request) {
		return
	}
	if !a.featureCollectionOwner(user, request.ID, true) {
		fail(w, 404, "合集不存在")
		return
	}
	if len(request.Items) > 500 {
		fail(w, 400, "一次最多操作 500 项")
		return
	}
	tx, err := a.db.Begin()
	if err != nil {
		featureError(w, err)
		return
	}
	defer tx.Rollback()
	for _, item := range request.Items {
		if _, e := a.featureAccessibleItem(user, item); e != nil {
			featureError(w, e)
			return
		}
		if r.Method == http.MethodPost {
			_, err = tx.Exec("INSERT INTO feature_collection_items VALUES(?,?,?) ON CONFLICT DO NOTHING", request.ID, item, featureNow())
		} else {
			_, err = tx.Exec("DELETE FROM feature_collection_items WHERE collection=? AND item=?", request.ID, item)
		}
		if err != nil {
			featureError(w, err)
			return
		}
	}
	if err = tx.Commit(); err != nil {
		featureError(w, err)
		return
	}
	respond(w, M{"ok": true})
}
func (a *App) featureNetworkAPI(w http.ResponseWriter, r *http.Request) {
	if !featureMethod(w, r, http.MethodGet, http.MethodPut) {
		return
	}
	c := featureNetworkSettings{}
	a.featureSetting("network", &c)
	if r.Method == http.MethodPut {
		if !body(w, r, &c) {
			return
		}
		if len(c.AllowedHosts) > 50 || len(c.AllowedOrigins) > 50 {
			fail(w, 400, "最多配置 50 项")
			return
		}
		for _, host := range c.AllowedHosts {
			if strings.ContainsAny(host, "/\\\r\n ") || host == "" {
				fail(w, 400, "访问主机格式无效")
				return
			}
		}
		if c.PublicURL != "" && !strings.HasPrefix(c.PublicURL, "https://") && !strings.HasPrefix(c.PublicURL, "http://") {
			fail(w, 400, "外部访问地址无效")
			return
		}
		if err := a.saveFeatureSetting("network", c); err != nil {
			featureError(w, err)
			return
		}
	}
	respond(w, c)
}

type featureNetworkSettings struct {
	PublicURL      string
	AllowedHosts   []string
	AllowedOrigins []string
}

func featureParseDate(raw string) time.Time {
	for _, format := range []string{time.RFC3339Nano, "2006-01-02T15:04:05", "2006-01-02"} {
		if t, err := time.Parse(format, raw); err == nil {
			return t
		}
	}
	return time.Time{}
}

func (a *App) featureDTO(x Item, user User) M {
	d := a.dto(x)
	m := a.featureMetadataView(x)
	d["Name"] = m.Title
	d["Overview"] = m.Plot
	d["Genres"] = m.Genres
	d["Tags"] = m.Tags
	d["ProductionLocations"] = m.Countries
	d["CommunityRating"] = m.Rating
	d["ProductionYear"] = m.Year
	var position int64
	var played, favorite int
	_ = a.db.QueryRow("SELECT position,played FROM userdata WHERE user_id=? AND item=?", user.ID, x.ID).Scan(&position, &played)
	_ = a.db.QueryRow("SELECT favorite FROM userdata_extra WHERE user_id=? AND item=?", user.ID, x.ID).Scan(&favorite)
	d["UserData"] = M{"PlaybackPositionTicks": position, "Played": played == 1, "IsFavorite": favorite == 1}
	if a.featureHasArtwork(x, "Primary") {
		d["ImageTags"] = M{"Primary": "custom"}
		d["PrimaryImageTag"] = "custom"
	}
	return d
}
