package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
)

func (a *App) authorizedImageTag(user User, tag string) string {
	if user.ID == "" || user.API || tag == "" {
		return tag
	}
	value := tag + "." + hex.EncodeToString([]byte(user.ID))
	mac := hmac.New(sha256.New, []byte(a.cursorSecret))
	mac.Write([]byte("image-viewer|" + value))
	return value + "." + hex.EncodeToString(mac.Sum(nil))[:32]
}

func (a *App) applyViewerImageTags(dto M, user User) {
	for key, value := range dto {
		if key == "ImageTags" {
			if tags, ok := value.(M); ok {
				for kind, tag := range tags {
					if text, ok := tag.(string); ok && !strings.Contains(text, ".") {
						tags[kind] = a.authorizedImageTag(user, text)
					}
				}
			}
		} else if strings.HasSuffix(key, "ImageTags") {
			if tags, ok := value.([]string); ok {
				for i, tag := range tags {
					if !strings.Contains(tag, ".") {
						tags[i] = a.authorizedImageTag(user, tag)
					}
				}
			}
		} else if strings.HasSuffix(key, "ImageTag") {
			if tag, ok := value.(string); ok && !strings.Contains(tag, ".") {
				dto[key] = a.authorizedImageTag(user, tag)
			}
		}
	}
	if people, ok := dto["People"].([]M); ok {
		for _, person := range people {
			a.applyViewerImageTags(person, user)
		}
	}
}

func (a *App) visibleImage(r *http.Request, itemID string) bool {
	if itemID == "favorites" {
		user, _ := r.Context().Value(mediaViewerKey{}).(User)
		return user.ID != "" && !user.API && a.favoritesEnabled()
	}
	var exists int
	return a.mediaReader(r).QueryRow("SELECT 1 FROM items WHERE id=? UNION ALL SELECT 1 FROM libraries WHERE id=? UNION ALL SELECT 1 FROM item_people WHERE person=? LIMIT 1", itemID, itemID, itemID).Scan(&exists) == nil
}

func (a *App) scopedTaggedImage(w http.ResponseWriter, r *http.Request, path string) bool {
	itemID, _, ok := imageRequest(path)
	if !ok || (r.Method != http.MethodGet && r.Method != http.MethodHead) {
		return false
	}
	tag := q(r, "tag")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if tag == "" && len(parts) > 5 {
		tag = parts[5]
	}
	fields := strings.Split(tag, ".")
	if len(fields) != 3 || len(fields[0]) != 32 || len(fields[1]) == 0 || len(fields[1]) > 256 {
		return false
	}
	decoded, err := hex.DecodeString(fields[1])
	if err != nil || len(decoded) == 0 {
		return false
	}
	user := User{ID: string(decoded)}
	if !hmac.Equal([]byte(tag), []byte(a.authorizedImageTag(user, fields[0]))) {
		return false
	}
	if a.db.QueryRow("SELECT admin FROM users WHERE id=?", user.ID).Scan(&user.Admin) != nil {
		return false
	}
	clone := r.Clone(context.WithValue(r.Context(), mediaViewerKey{}, user))
	urlCopy := *r.URL
	clone.URL = &urlCopy
	setQuery(clone, "tag", fields[0])
	if !a.visibleImage(clone, itemID) {
		return false
	}
	return a.taggedImage(w, clone, path)
}
