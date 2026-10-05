package main

import (
	"net/http"
	"strings"
)

func favoriteQuery(r *http.Request) bool {
	if strings.EqualFold(q(r, "IsFavorite"), "true") {
		return true
	}
	for _, filter := range strings.Split(q(r, "Filters"), ",") {
		if strings.EqualFold(strings.TrimSpace(filter), "IsFavorite") {
			return true
		}
	}
	return false
}

func (a *App) userLibraryDTOs(r *http.Request, user User) []M {
	visible := map[string]bool{}
	rows, err := a.mediaForUser(r.Context(), user).Query("SELECT id FROM libraries")
	if err != nil {
		return []M{}
	}
	for rows.Next() {
		var id string
		if rows.Scan(&id) != nil {
			rows.Close()
			return []M{}
		}
		visible[id] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return []M{}
	}
	out := []M{}
	for _, library := range a.libraryDTOs() {
		if visible[library["Id"].(string)] {
			if source := a.imagePathForViewer(r, library["Id"].(string), "Primary"); source != "" {
				library["ImageTags"] = M{"Primary": a.imageTag(library["Id"].(string), "Primary", source)}
				if strings.HasPrefix(source, "collage:") {
					library["PrimaryImageAspectRatio"] = 16.0 / 9
				}
			} else {
				library["ImageTags"] = M{}
			}
			a.applyViewerImageTags(library, user)
			out = append(out, library)
		}
	}
	if !user.API && a.favoritesEnabled() {
		var count int
		if a.mediaForUser(r.Context(), user).QueryRow("SELECT count(*) FROM items WHERE kind IN ('Movie','Series') AND id IN (SELECT item FROM userdata_extra WHERE user_id=? AND favorite=1)", user.ID).Scan(&count) != nil {
			return out
		}
		favorite := M{"Id": "favorites", "Name": "收藏", "Type": "CollectionFolder", "IsFolder": true, "CollectionType": "movies", "LocationType": "Virtual", "ParentId": "root", "ServerId": a.serverID, "DisplayPreferencesId": "favorites", "ImageTags": a.libraryImageTagsForViewer(r, "favorites"), "PrimaryImageAspectRatio": 16.0 / 9, "BackdropImageTags": []string{}, "ChildCount": count, "RecursiveItemCount": count, "CanDelete": false, "CanDownload": false, "UserData": M{"IsFavorite": false, "Played": false, "PlaybackPositionTicks": 0}}
		a.applyViewerImageTags(favorite, user)
		out = append(out, favorite)
	}
	return out
}

func (a *App) userRootDTO(r *http.Request, user User) M {
	root := a.rootDTO()
	root["ChildCount"] = len(a.userLibraryDTOs(r, user))
	return root
}

func (a *App) respondUserLibraries(w http.ResponseWriter, r *http.Request, user User) {
	items := a.userLibraryDTOs(r, user)
	respond(w, M{"Items": items, "TotalRecordCount": len(items), "StartIndex": 0})
}
