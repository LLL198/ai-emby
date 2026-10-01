package main

import (
	"bytes"
	"database/sql"
	"errors"
	"image"
	"io"
	"net/http"
)

// 0x85e680. Avatar changes require the user's own session, including for an
// administrator. Administrators can read another user's avatar.
func (a *App) userAvatar(w http.ResponseWriter, r *http.Request, user User, userID string) {
	if user.API || userID == "" {
		fail(w, http.StatusForbidden, "无权限")
		return
	}
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodDelete:
	default:
		fail(w, http.StatusMethodNotAllowed, "不支持此方法")
		return
	}
	if (r.Method == http.MethodPost || r.Method == http.MethodDelete) && userID != user.ID {
		fail(w, http.StatusForbidden, "只能管理自己的头像")
		return
	}
	var exists int
	if err := a.db.QueryRow("SELECT 1 FROM users WHERE id=?", userID).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			fail(w, http.StatusNotFound, "用户不存在")
		} else {
			fail(w, http.StatusInternalServerError, "读取失败")
		}
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	switch r.Method {
	case http.MethodDelete:
		if err := deleteUserImage("images-tx", userID); err != nil {
			fail(w, http.StatusInternalServerError, "头像删除失败")
			return
		}
		if _, err := a.db.Exec("DELETE FROM user_avatars WHERE user_id=?", userID); err != nil {
			fail(w, http.StatusInternalServerError, "头像删除失败")
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	case http.MethodPost:
		data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, (5<<20)+1))
		if err != nil || len(data) > 5<<20 {
			fail(w, http.StatusRequestEntityTooLarge, "头像最大 5MB")
			return
		}
		mime := http.DetectContentType(data)
		if mime != "image/jpeg" && mime != "image/png" && mime != "image/webp" {
			fail(w, http.StatusBadRequest, "仅支持 JPEG、PNG、WebP")
			return
		}
		config, format, err := image.DecodeConfig(bytes.NewReader(data))
		if err != nil || config.Width <= 0 || config.Height <= 0 ||
			int64(config.Width)*int64(config.Height) > 12_000_000 ||
			(format != "jpeg" && format != "png" && format != "webp") || mime != "image/"+format {
			fail(w, http.StatusBadRequest, "图片无效或像素过大")
			return
		}
		if err := saveUserImage("images-tx", userID, mime, data); err != nil {
			fail(w, http.StatusInternalServerError, "头像保存失败")
			return
		}
		_, _ = a.db.Exec("DELETE FROM user_avatars WHERE user_id=?", userID)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if userID != user.ID && !user.Admin {
		fail(w, http.StatusForbidden, "无权限")
		return
	}
	if path, ok := userImageFile("images-tx", userID); ok {
		if err := serveUserImage(w, r, path); err != nil {
			fail(w, http.StatusInternalServerError, "读取失败")
		}
		return
	}
	var mime string
	var data []byte
	if err := a.db.QueryRow("SELECT mime,data FROM user_avatars WHERE user_id=?", userID).Scan(&mime, &data); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			fail(w, http.StatusNotFound, "没有自定义头像")
		} else {
			fail(w, http.StatusInternalServerError, "读取失败")
		}
		return
	}
	w.Header().Set("Content-Type", mime)
	if r.Method == http.MethodGet {
		_, _ = w.Write(data)
	}
}
