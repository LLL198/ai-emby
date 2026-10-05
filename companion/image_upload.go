package main

import (
	"bytes"
	"encoding/base64"
	"io"
	"net/http"
	"strings"
)

// Image writes require a logged-in administrator or an existing integration key.
// Keep public signed-image GET/HEAD handling separate from mutations.
func (a *App) imageUploadRoute(w http.ResponseWriter, r *http.Request, u User, p string) bool {
	id, kind, ok := imageRequest(p)
	if !ok || r.Method != http.MethodPost {
		return false
	}
	if !u.Admin && !u.API {
		fail(w, 403, "需要管理员或API密钥")
		return true
	}
	parts := strings.Split(strings.Trim(p, "/"), "/")
	if kind != "Primary" || len(parts) > 5 || (len(parts) == 5 && parts[4] != "0") || (q(r, "Index") != "" && q(r, "Index") != "0") {
		fail(w, 400, "仅支持主封面（索引0）上传")
		return true
	}
	var exists int
	if a.db.QueryRow("SELECT 1 FROM libraries WHERE id=? UNION ALL SELECT 1 FROM items WHERE id=? LIMIT 1", id, id).Scan(&exists) != nil {
		fail(w, 404, "媒体不存在")
		return true
	}
	const limit = 5 << 20
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, int64(base64.StdEncoding.EncodedLen(limit)+4096)))
	if err != nil {
		fail(w, 413, "封面最大5MB")
		return true
	}
	mime := http.DetectContentType(data)
	if !coverMIME(mime) {
		data, err = base64.StdEncoding.DecodeString(string(bytes.TrimSpace(data)))
		if err != nil {
			fail(w, 400, "无效图片或Base64数据")
			return true
		}
		mime = http.DetectContentType(data)
	}
	if len(data) > limit {
		fail(w, 413, "封面最大5MB")
		return true
	}
	if !coverMIME(mime) {
		fail(w, 400, "仅支持JPEG、PNG、WebP")
		return true
	}
	a.write.Lock()
	_, err = a.db.Exec("INSERT INTO covers(id,mime,data) VALUES(?,?,?) ON CONFLICT(id) DO UPDATE SET mime=excluded.mime,data=excluded.data", id, mime, data)
	a.write.Unlock()
	if err != nil {
		fail(w, 500, "保存失败")
		return true
	}
	w.WriteHeader(http.StatusNoContent)
	return true
}
func coverMIME(mime string) bool {
	return mime == "image/jpeg" || mime == "image/png" || mime == "image/webp"
}
