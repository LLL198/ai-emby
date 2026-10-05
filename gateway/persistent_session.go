package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	_ "github.com/lib/pq"
)

// Extend an existing valid device login; logout and account changes revoke the token.
const rememberedTokenExpiry int64 = 253402300799 // End of year 9999.

func openSessionDatabase() (*sql.DB, error) {
	db, err := sql.Open("postgres", os.Getenv("DATABASE_URL"))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(5 * time.Minute)
	return db, nil
}

func sessionError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"Message": message})
}

func persistDeviceSession(db *sql.DB, w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		sessionError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	accessToken := strings.TrimSpace(r.Header.Get("X-Emby-Token"))
	if accessToken == "" {
		sessionError(w, http.StatusUnauthorized, "请先登录")
		return
	}
	var input struct{ DeviceId string }
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
	if err := decoder.Decode(&input); err != nil || input.DeviceId == "" || len(input.DeviceId) > 256 {
		sessionError(w, http.StatusBadRequest, "设备信息无效")
		return
	}
	// /Users/Me checks authorization and identifies the session owner.
	authorized, err := get(r, coreURL, "/emby/Users/Me")
	if err != nil {
		sessionError(w, http.StatusBadGateway, "登录服务暂时不可用，请稍后重试")
		return
	}
	if authorized.StatusCode != http.StatusOK {
		relay(w, authorized)
		return
	}
	userJSON, err := io.ReadAll(io.LimitReader(authorized.Body, 1<<20))
	authorized.Body.Close()
	var user struct{ Id string }
	if err != nil || json.Unmarshal(userJSON, &user) != nil || user.Id == "" {
		sessionError(w, http.StatusBadGateway, "登录服务返回无效用户")
		return
	}
	if db == nil {
		sessionError(w, http.StatusServiceUnavailable, "暂时无法保存长期登录，请稍后重试")
		return
	}
	digest := sha256.Sum256([]byte(accessToken))
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	var expiry int64
	// UPDATE never recreates a revoked row, never revives an expired token and
	// cannot touch any other device or token. Repeated page visits avoid writes.
	err = db.QueryRowContext(ctx, `UPDATE tokens SET expires=$1
		WHERE hash=$2 AND user_id=$3 AND device=$4 AND expires>$5 AND expires<$1
		RETURNING expires`, rememberedTokenExpiry, hex.EncodeToString(digest[:]),
		user.Id, input.DeviceId, time.Now().Unix()).Scan(&expiry)
	if err == sql.ErrNoRows {
		err = db.QueryRowContext(ctx, `SELECT expires FROM tokens
			WHERE hash=$1 AND user_id=$2 AND device=$3 AND expires>$4`,
			hex.EncodeToString(digest[:]), user.Id, input.DeviceId, time.Now().Unix()).Scan(&expiry)
	}
	if err == sql.ErrNoRows {
		sessionError(w, http.StatusUnauthorized, "当前设备登录已失效，请重新登录")
		return
	}
	if err != nil {
		// Database errors may include connection details; don't log them.
		sessionError(w, http.StatusServiceUnavailable, "暂时无法保存长期登录，请稍后重试")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"User": json.RawMessage(userJSON), "Persistent": expiry >= rememberedTokenExpiry})
}
