package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

type managedUserPolicy struct {
	IsAdministrator         *bool
	Password                string
	SimultaneousStreamLimit *int
	EnableMediaPlayback     *bool
	CanViewHiddenLibraries  *bool
	IsDisabled              bool
}

func (a *App) userManagement(w http.ResponseWriter, r *http.Request, current User, path string) bool {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	operation, userID := "", ""
	switch {
	case strings.EqualFold(path, "/Sessions"):
		operation = "sessions"
	case strings.EqualFold(path, "/Users/Query"):
		operation = "query"
	case strings.EqualFold(path, "/Users/New"):
		operation = "new"
	case len(parts) == 2 && strings.EqualFold(parts[0], "users") && r.Method == http.MethodDelete:
		operation, userID = "delete", parts[1]
	case len(parts) == 3 && strings.EqualFold(parts[0], "users"):
		operation, userID = strings.ToLower(parts[2]), parts[1]
		if operation != "policy" && operation != "configuration" && operation != "password" && operation != "delete" {
			return false
		}
	default:
		return false
	}
	if operation == "password" && !current.Admin && !current.API {
		return false
	}
	if !current.Admin && !current.API {
		fail(w, 403, "需要管理员或 API key")
		return true
	}
	method := http.MethodPost
	if operation == "query" || operation == "sessions" {
		method = http.MethodGet
	} else if operation == "delete" && len(parts) == 2 {
		method = http.MethodDelete
	}
	if r.Method != method {
		fail(w, 405, method+" required")
		return true
	}
	w.Header().Set("Cache-Control", "no-store")
	if operation == "sessions" {
		a.managementSessions(w, r)
		return true
	}
	if operation == "query" {
		users := a.users()
		respond(w, M{"Items": users, "TotalRecordCount": len(users)})
		return true
	}
	a.write.Lock()
	defer a.write.Unlock()
	if operation == "new" {
		var input struct {
			Name     string
			Password *string
		}
		if !body(w, r, &input) {
			return true
		}
		input.Name = strings.TrimSpace(input.Name)
		if input.Name == "" {
			fail(w, 400, "账号不能为空")
			return true
		}
		password := id() + id()
		if input.Password != nil {
			password = *input.Password
		}
		if len(password) > 72 {
			fail(w, 400, "密码不能超过 72 字节")
			return true
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(password), 12)
		if err != nil {
			fail(w, 500, "密码生成失败")
			return true
		}
		user := User{ID: id(), Name: input.Name, Hash: string(hash), Max: 1}
		_, err = a.db.Exec("INSERT INTO users(id,name,hash,admin,first_admin,max_devices) VALUES(?,?,?,?,?,?)", user.ID, user.Name, user.Hash, false, false, user.Max)
		if err != nil {
			fail(w, 409, "用户名重复或保存失败")
			return true
		}
		respond(w, a.userDTO(user))
		return true
	}
	if operation == "delete" {
		if a.deleteUserCompletely(w, current, userID) {
			w.WriteHeader(http.StatusNoContent)
		}
		return true
	}
	var user User
	if err := a.db.QueryRow("SELECT id,name,hash,admin,first_admin,max_devices FROM users WHERE id=?", userID).Scan(&user.ID, &user.Name, &user.Hash, &user.Admin, &user.First, &user.Max); err != nil {
		fail(w, 404, "用户不存在")
		return true
	}
	switch operation {
	case "policy":
		var policy managedUserPolicy
		if !body(w, r, &policy) {
			return true
		}
		if !a.saveManagedUserPolicy(w, userID, policy) {
			return true
		}
	case "configuration":
		var configuration M
		if !body(w, r, &configuration) {
			return true
		}
		if configuration == nil {
			fail(w, 400, "配置不能为空")
			return true
		}
		data, err := json.Marshal(configuration)
		if err == nil {
			_, err = a.db.Exec("INSERT INTO settings(k,v) VALUES(?,?) ON CONFLICT(k) DO UPDATE SET v=excluded.v", "user-config:"+userID, string(data))
		}
		if err != nil {
			fail(w, 500, "保存失败")
			return true
		}
	case "password":
		if user.Admin || userID == current.ID {
			a.password(w, r, current, userID)
			return true
		}
		var input struct {
			NewPw         string
			ResetPassword bool
		}
		if !body(w, r, &input) {
			return true
		}
		if input.ResetPassword {
			input.NewPw = ""
		}
		if len(input.NewPw) > 72 {
			fail(w, 400, "密码不能超过 72 字节")
			return true
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(input.NewPw), 12)
		if err != nil {
			fail(w, 500, "密码生成失败")
			return true
		}
		tx, err := a.db.Begin()
		if err != nil {
			fail(w, 500, "保存失败")
			return true
		}
		defer tx.Rollback()
		result, err := tx.Exec("UPDATE users SET hash=? WHERE id=? AND admin=0 AND first_admin=0", string(hash), userID)
		if err == nil {
			var count int64
			count, err = result.RowsAffected()
			if err == nil && count == 0 {
				fail(w, 409, "用户权限已变更，请刷新后重新操作")
				return true
			}
		}
		if err == nil {
			_, err = tx.Exec("DELETE FROM tokens WHERE user_id=?", userID)
		}
		if err == nil {
			err = tx.Commit()
		}
		if err != nil {
			fail(w, 500, "保存失败")
			return true
		}
	}
	w.WriteHeader(http.StatusNoContent)
	return true
}

func (a *App) saveManagedUserPolicy(w http.ResponseWriter, userID string, policy managedUserPolicy) bool {
	if policy.IsDisabled {
		fail(w, 400, "禁用账号尚不支持，请使用播放权限或删除账号")
		return false
	}
	tx, err := a.db.Begin()
	if err != nil {
		fail(w, 500, "保存失败")
		return false
	}
	defer tx.Rollback()
	var user User
	err = tx.QueryRow("SELECT admin,first_admin,max_devices FROM users WHERE id=? FOR UPDATE", userID).Scan(&user.Admin, &user.First, &user.Max)
	if errors.Is(err, sql.ErrNoRows) {
		fail(w, 404, "用户不存在")
		return false
	}
	if err != nil {
		fail(w, 500, "读取用户失败")
		return false
	}
	admin, devices := user.Admin, user.Max
	if policy.IsAdministrator != nil {
		admin = *policy.IsAdministrator
	}
	if user.First {
		admin = true
	}
	if policy.SimultaneousStreamLimit != nil {
		devices = *policy.SimultaneousStreamLimit
	}
	if devices < 1 || devices > 100 {
		fail(w, 400, "设备数量范围 1–100")
		return false
	}
	var hash []byte
	if admin && !user.Admin {
		if len(policy.Password) < 12 || len(policy.Password) > 72 {
			fail(w, 400, "提升管理员时需要设置 12–72 字节密码")
			return false
		}
		hash, err = bcrypt.GenerateFromPassword([]byte(policy.Password), 12)
		if err != nil {
			fail(w, 500, "密码生成失败")
			return false
		}
	}
	if _, err = tx.Exec("UPDATE users SET admin=?,max_devices=? WHERE id=?", admin, devices, userID); err == nil && len(hash) != 0 {
		_, err = tx.Exec("UPDATE users SET hash=? WHERE id=?", string(hash), userID)
	}
	if err == nil && (admin != user.Admin || len(hash) != 0) {
		_, err = tx.Exec("DELETE FROM tokens WHERE user_id=?", userID)
	}
	if err == nil && policy.CanViewHiddenLibraries != nil {
		_, err = tx.Exec("UPDATE users SET can_view_hidden_libraries=? WHERE id=?", *policy.CanViewHiddenLibraries, userID)
	}
	if err == nil && policy.EnableMediaPlayback != nil {
		_, err = tx.Exec("INSERT INTO user_playback(user_id,allowed) VALUES(?,?) ON CONFLICT(user_id) DO UPDATE SET allowed=excluded.allowed", userID, *policy.EnableMediaPlayback)
		if err == nil && !*policy.EnableMediaPlayback {
			_, err = tx.Exec("DELETE FROM plays WHERE user_id=?", userID)
		}
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		fail(w, 500, "保存失败")
		return false
	}
	return true
}

func (a *App) managedUserConfiguration(userID string, defaults M) M {
	if defaults == nil {
		defaults = M{}
	}
	var data string
	if a.db.QueryRow("SELECT v FROM settings WHERE k=?", "user-config:"+userID).Scan(&data) == nil {
		var saved M
		if json.Unmarshal([]byte(data), &saved) == nil {
			for key, value := range saved {
				defaults[key] = value
			}
		}
	}
	return defaults
}

func (a *App) managementSessions(w http.ResponseWriter, r *http.Request) {
	now := time.Now().Unix()
	rows, err := a.db.Query("SELECT DISTINCT u.id,u.name,t.device,COALESCE(l.last_login,0),COALESCE(p.item,''),COALESCE(p.updated,0),COALESCE(i.name,'') FROM tokens t JOIN users u ON u.id=t.user_id LEFT JOIN user_logins l ON l.user_id=u.id LEFT JOIN plays p ON p.user_id=u.id AND p.device=t.device AND p.updated>=? LEFT JOIN items i ON i.id=p.item WHERE t.expires>?", now-180, now)
	if err != nil {
		fail(w, 500, "读取会话失败")
		return
	}
	defer rows.Close()
	devices := map[string]activityEntry{}
	a.activity.mu.Lock()
	for _, entry := range a.activity.entries {
		if entry.Category == "playback" {
			key := entry.UserID + ":" + entry.DeviceKey
			if previous, exists := devices[key]; !exists || entry.Updated.After(previous.Updated) {
				devices[key] = *entry
			}
		}
	}
	a.activity.mu.Unlock()
	sessions := []M{}
	for rows.Next() {
		var userID, name, deviceID, itemID, itemName string
		var login, updated int64
		if err = rows.Scan(&userID, &name, &deviceID, &login, &itemID, &updated, &itemName); err != nil {
			fail(w, 500, "读取会话失败")
			return
		}
		key := userID + ":" + deviceID
		session := M{"Id": digest(key), "UserId": userID, "UserName": name, "DeviceId": deviceID, "DeviceName": deviceID,
			"LastActivityDate": time.Unix(max(login, updated), 0).UTC().Format(time.RFC3339)}
		if itemID != "" {
			session["NowPlayingItem"] = M{"Id": itemID, "Name": itemName}
		}
		if entry, exists := devices[key]; exists {
			if entry.Device != "" {
				session["DeviceName"] = entry.Device
			}
			session["Client"], session["RemoteEndPoint"] = entry.Client, entry.IP
		}
		sessions = append(sessions, session)
	}
	if rows.Err() != nil {
		fail(w, 500, "读取会话失败")
		return
	}
	respond(w, sessions)
}
