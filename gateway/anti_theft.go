package main

import (
	"context"
	"database/sql"
	"net/http"
	"strconv"
	"time"

	"github.com/LLL198/ai-emby/security"
)

func enforceAntiTheft(db *sql.DB, w http.ResponseWriter, r *http.Request) bool {
	if r.Method == http.MethodOptions || security.Token(r) == "" {
		return false
	}
	// Allow a banned client to log out and release its playback reservation.
	if security.CleanupRequest(r) {
		return false
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	user, ok := featureGatewayIdentity(db, r.WithContext(ctx))
	if !ok || user.ID == "" || user.Admin {
		return false
	}
	now := time.Now()
	decision, err := security.Admit(ctx, db, user.ID, security.PlaybackEntry(r), now)
	if err != nil {
		sessionError(w, 503, "防盗保护校验暂时不可用，请重试")
		return true
	}
	if decision.Warning {
		w.Header().Set("X-AI-Emby-Protection-Warning", "playback-rate-limit")
	}
	if decision.Banned(now) {
		w.Header().Set("Cache-Control", "no-store")
		if !decision.Permanent {
			w.Header().Set("Retry-After", strconv.FormatInt(decision.RetrySeconds(now), 10))
		}
		sessionError(w, http.StatusForbidden, decision.Message())
		return true
	}
	return false
}
