package main

import (
	"bytes"
	"fmt"
	"image"
	"io"
	"net/http"
	"strings"
	"time"
)

func (a *App) favoriteCoverRevision(userID string) string {
	var revision string
	_ = a.db.QueryRow("SELECT v FROM settings WHERE k=?", "favorite-cover-rev:"+digest(userID)).Scan(&revision)
	return revision
}

func (a *App) favoriteCollageRevision(reader *mediaReader) string {
	var epoch string
	var count int
	_ = a.db.QueryRow("SELECT v FROM settings WHERE k='favorite-cover-epoch'").Scan(&epoch)
	_ = reader.QueryRow("SELECT count(*) FROM items WHERE kind IN ('Movie','Series') AND id IN (SELECT item FROM userdata_extra WHERE user_id=? AND favorite=1)", reader.viewer.ID).Scan(&count)
	return fmt.Sprintf("%s:%s:%d:%s", epoch, a.favoriteCoverRevision(reader.viewer.ID), count, collageVisibilityRevision(reader))
}

func (a *App) storeFavoriteCover(userID, mime string, data []byte, remove bool) error {
	a.write.Lock()
	defer a.write.Unlock()
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var exists int
	if err = tx.QueryRow("SELECT 1 FROM users WHERE id=? FOR UPDATE", userID).Scan(&exists); err != nil {
		return err
	}
	if remove {
		err = deleteUserImage("images-sc", userID)
	} else {
		err = saveUserImage("images-sc", userID, mime, data)
	}
	if err != nil {
		return err
	}
	if _, err = tx.Exec("DELETE FROM covers WHERE id=?", "favorite-cover:"+digest(userID)); err != nil {
		return err
	}
	if _, err = tx.Exec("INSERT INTO settings(k,v) VALUES(?,?) ON CONFLICT(k) DO UPDATE SET v=excluded.v", "favorite-cover-rev:"+digest(userID), id()); err != nil {
		return err
	}
	return tx.Commit()
}

func (a *App) favoriteCoverRoute(w http.ResponseWriter, r *http.Request, user User, path string) bool {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if (len(parts) != 3 && len(parts) != 4) || !strings.EqualFold(parts[0], "users") || !strings.EqualFold(parts[2], "favoritecover") || (len(parts) == 4 && !strings.EqualFold(parts[3], "image")) {
		return false
	}
	userID := parts[1]
	if strings.EqualFold(userID, "me") {
		userID = user.ID
	}
	if user.API || userID == "" || userID != user.ID {
		fail(w, 403, "只能管理自己的收藏封面")
		return true
	}
	w.Header().Set("Cache-Control", "private, no-store")
	coverID := "favorite-cover:" + digest(userID)
	if len(parts) == 4 {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			fail(w, 405, "GET required")
			return true
		}
		if file, ok := userImageFile("images-sc", userID); ok {
			if err := serveUserImage(w, r, file); err != nil {
				fail(w, 500, "读取失败")
			}
			return true
		}
		var data []byte
		var mime string
		if err := a.db.QueryRow("SELECT data,mime FROM covers WHERE id=?", coverID).Scan(&data, &mime); err != nil {
			fail(w, 404, "没有自定义封面")
			return true
		}
		w.Header().Set("Content-Type", mime)
		http.ServeContent(w, r, "favorite-cover", time.Time{}, bytes.NewReader(data))
		return true
	}
	switch r.Method {
	case http.MethodGet:
		_, present := userImageFile("images-sc", userID)
		if !present {
			var exists int
			present = a.db.QueryRow("SELECT 1 FROM covers WHERE id=?", coverID).Scan(&exists) == nil
		}
		respond(w, M{"HasCustomCover": present})
	case http.MethodPost:
		data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, (5<<20)+1))
		if err != nil || len(data) > 5<<20 {
			fail(w, 413, "封面最大5MB")
			return true
		}
		mime := http.DetectContentType(data)
		if !coverMIME(mime) {
			fail(w, 400, "仅支持JPEG、PNG、WebP")
			return true
		}
		config, format, err := image.DecodeConfig(bytes.NewReader(data))
		if err != nil || config.Width <= 0 || config.Height <= 0 || int64(config.Width)*int64(config.Height) > 12_000_000 || (format != "jpeg" && format != "png" && format != "webp") || mime != "image/"+format {
			fail(w, 400, "图片无效或尺寸过大")
			return true
		}
		if err := a.storeFavoriteCover(userID, mime, data, false); err != nil {
			fail(w, 500, "保存封面失败")
			return true
		}
		w.WriteHeader(http.StatusNoContent)
	case http.MethodDelete:
		if err := a.storeFavoriteCover(userID, "", nil, true); err != nil {
			fail(w, 500, "清除封面失败")
			return true
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		fail(w, 405, "不支持此方法")
	}
	return true
}

func (a *App) favoriteImagePath(reader *mediaReader) string {
	user := reader.viewer
	if user.ID == "" || user.API || !a.favoritesEnabled() {
		return ""
	}
	coverID := "favorite-cover:" + digest(user.ID)
	_, present := userImageFile("images-sc", user.ID)
	if !present {
		var exists int
		present = a.db.QueryRow("SELECT 1 FROM covers WHERE id=?", coverID).Scan(&exists) == nil
	}
	if present {
		return "cover:" + coverID + "|" + a.favoriteCoverRevision(user.ID)
	}
	return "collage:favorites|" + digest(user.ID) + "|" + a.favoriteCollageRevision(reader)
}
