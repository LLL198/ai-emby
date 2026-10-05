package main

import "net/http"

func (a *App) deleteUserCompletely(w http.ResponseWriter, current User, userID string) bool {
	tx, err := a.db.Begin()
	if err != nil {
		fail(w, 500, "删除失败")
		return false
	}
	defer tx.Rollback()
	var admin, first bool
	if err = tx.QueryRow("SELECT admin,first_admin FROM users WHERE id=? FOR UPDATE", userID).Scan(&admin, &first); err != nil {
		fail(w, 404, "用户不存在")
		return false
	}
	if admin || first || userID == current.ID {
		fail(w, 403, "管理员账号或当前账号不能删除")
		return false
	}
	for _, key := range []string{"favorite-cover-rev:" + userID, "favorite-cover-rev:" + digest(userID), "user-config:" + userID} {
		if _, err = tx.Exec("DELETE FROM settings WHERE k=?", key); err != nil {
			fail(w, 500, "删除失败")
			return false
		}
	}
	if _, err = tx.Exec("DELETE FROM covers WHERE id=?", "favorite-cover:"+digest(userID)); err == nil {
		_, err = tx.Exec("DELETE FROM users WHERE id=?", userID)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		fail(w, 500, "删除失败")
		return false
	}
	if err = deleteUserImage("images-tx", userID); err != nil {
		fail(w, 500, "账号已删除，头像清理失败")
		return false
	}
	if err = deleteUserImage("images-sc", userID); err != nil {
		fail(w, 500, "账号已删除，收藏封面清理失败")
		return false
	}
	return true
}
