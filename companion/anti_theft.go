package main

import (
	"context"
	"database/sql"
	"net/http"
	"strconv"
	"time"

	"github.com/LLL198/ai-emby/security"
)

type accountBanError struct{ security.Decision }

func (e accountBanError) Error() string { return e.Message() }

func writeAccountBan(w http.ResponseWriter, ban security.Decision) {
	if !ban.Permanent {
		w.Header().Set("Retry-After", strconv.FormatInt(ban.RetrySeconds(time.Now()), 10))
	}
	fail(w, http.StatusForbidden, ban.Message())
}

func (a *App) antiTheftUserBanned(userID string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	ban, err := security.Ban(ctx, a.db.DB, userID)
	return err == nil && ban.Banned(time.Now())
}

func (a *App) antiTheftAPI(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if r.URL.Path != "/admin/features/anti-theft" {
		if r.URL.Path != "/admin/features/anti-theft/account" || !featureMethod(w, r, http.MethodPut, http.MethodPost) {
			return
		}
		var input struct {
			UserID            string
			RequestsPerMinute *int
		}
		if !body(w, r, &input) {
			return
		}
		if input.UserID == "" {
			fail(w, 400, "请选择账号")
			return
		}
		err := security.UpdateAccount(ctx, a.db.DB, input.UserID, input.RequestsPerMinute, r.Method == http.MethodPost, time.Now())
		if err != nil {
			fail(w, 400, err.Error())
			return
		}
		respond(w, M{"ok": true})
		return
	}
	if !featureMethod(w, r, http.MethodGet, http.MethodPut) {
		return
	}
	c, err := security.Load(ctx, a.db.DB)
	if err != nil {
		featureError(w, err)
		return
	}
	if r.Method == http.MethodPut {
		if !body(w, r, &c) {
			return
		}
		if err = security.Validate(c); err != nil {
			fail(w, 400, err.Error())
			return
		}
		if err = a.saveFeatureSetting("anti-theft", c); err != nil {
			featureError(w, err)
			return
		}
	}
	rows, err := a.db.DB.QueryContext(ctx, `SELECT u.id,u.name,u.admin,a.request_limit,
	 COALESCE(a.permanent,false),COALESCE(a.blocked_until,0)
	 FROM users u LEFT JOIN anti_theft_accounts a ON a.user_id=u.id ORDER BY u.admin DESC,u.name,u.id`)
	if err != nil {
		featureError(w, err)
		return
	}
	accounts := []M{}
	for rows.Next() {
		var uid, name string
		var admin int
		var limit sql.NullInt64
		var permanent bool
		var until int64
		if err = rows.Scan(&uid, &name, &admin, &limit, &permanent, &until); err != nil {
			break
		}
		var override any
		if limit.Valid {
			override = limit.Int64
		}
		blocked := admin == 0 && (permanent || until > time.Now().UnixMilli())
		accounts = append(accounts, M{"UserID": uid, "Name": name, "Admin": admin != 0, "RequestsPerMinute": override, "Banned": blocked, "Permanent": permanent, "BlockedUntil": antiTheftDate(until)})
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		featureError(w, err)
		return
	}
	rows, err = a.db.DB.QueryContext(ctx, `SELECT e.id,u.name,e.action,e.request_count,e.request_limit,e.created,e.blocked_until
	 FROM anti_theft_events e JOIN users u ON u.id=e.user_id ORDER BY e.created DESC,e.id DESC LIMIT 100`)
	if err != nil {
		featureError(w, err)
		return
	}
	events := []M{}
	for rows.Next() {
		var eid, count, limit, created, until int64
		var name, action string
		if err = rows.Scan(&eid, &name, &action, &count, &limit, &created, &until); err != nil {
			break
		}
		events = append(events, M{"ID": eid, "Name": name, "Action": action, "Count": count, "Limit": limit, "Created": antiTheftDate(created), "BlockedUntil": antiTheftDate(until)})
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		featureError(w, err)
		return
	}
	respond(w, M{"Settings": c, "Accounts": accounts, "Events": events})
}

func antiTheftDate(stamp int64) string {
	if stamp == 0 {
		return ""
	}
	return time.UnixMilli(stamp).UTC().Format(time.RFC3339Nano)
}
