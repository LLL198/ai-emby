package main

import (
	"golang.org/x/crypto/bcrypt"
	"net/http"
	"strings"
)

func (a *App) userIdentity(w http.ResponseWriter, r *http.Request) {
	if r.Method != "PUT" {
		fail(w, 405, "PUT required")
		return
	}
	var b struct{ ID, Name, Password string }
	if !body(w, r, &b) {
		return
	}
	b.Name = strings.TrimSpace(b.Name)
	if b.Name == "" || (b.Password != "" && (len(b.Password) < 10 || len(b.Password) > 72)) {
		fail(w, 400, "名称不能为空，密码需要10–72字节")
		return
	}
	tx, e := a.db.Begin()
	if e != nil {
		fail(w, 500, "保存失败")
		return
	}
	defer tx.Rollback()
	result, e := tx.Exec("UPDATE users SET name=? WHERE id=?", b.Name, b.ID)
	if e != nil {
		fail(w, 409, "用户名已存在或保存失败")
		return
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		fail(w, 404, "用户不存在")
		return
	}
	if b.Password != "" {
		h, err := bcrypt.GenerateFromPassword([]byte(b.Password), 12)
		if err != nil {
			fail(w, 400, "无效密码")
			return
		}
		if _, e = tx.Exec("UPDATE users SET hash=? WHERE id=?", string(h), b.ID); e == nil {
			_, e = tx.Exec("DELETE FROM tokens WHERE user_id=?", b.ID)
		}
		if e != nil {
			fail(w, 500, "密码保存失败")
			return
		}
	}
	if e = tx.Commit(); e != nil {
		fail(w, 500, "保存失败")
		return
	}
	respond(w, M{"ok": true})
}
