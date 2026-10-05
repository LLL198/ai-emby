package main

import (
	"net/http"
	"strconv"
)

func (a *App) queryVirtualFolders(w http.ResponseWriter, r *http.Request, user User) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		fail(w, 405, "GET required")
		return
	}
	start, limit := 0, -1
	for key, target := range map[string]*int{"StartIndex": &start, "Limit": &limit} {
		if text := q(r, key); text != "" {
			value, err := strconv.Atoi(text)
			if err != nil || value < 0 {
				fail(w, 400, "invalid "+key)
				return
			}
			*target = value
		}
	}
	reader := a.mediaForUser(r.Context(), user)
	rows, err := reader.Query("SELECT id FROM libraries")
	if err != nil {
		fail(w, 500, "媒体库查询失败")
		return
	}
	visible := map[string]bool{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			break
		}
		visible[id] = true
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		fail(w, 500, "媒体库读取失败")
		return
	}
	libraries := []M{}
	for _, library := range a.libraries() {
		if visible[library["Id"].(string)] {
			libraries = append(libraries, library)
		}
	}
	from := min(start, len(libraries))
	count := len(libraries) - from
	if limit >= 0 {
		count = min(count, limit)
	}
	respond(w, M{"Items": libraries[from : from+count], "TotalRecordCount": len(libraries), "StartIndex": start})
}
