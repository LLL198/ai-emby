// Package security shares protection policy between the public gateway and core.
package security

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/lib/pq"
)

const Schema = `
CREATE TABLE IF NOT EXISTS anti_theft_accounts (
 user_id TEXT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
 request_limit BIGINT,
 requests BIGINT[] NOT NULL DEFAULT '{}',
 warned_at BIGINT NOT NULL DEFAULT 0,
 blocked_until BIGINT NOT NULL DEFAULT 0,
 permanent BOOLEAN NOT NULL DEFAULT false
);
CREATE TABLE IF NOT EXISTS anti_theft_events (
 id BIGSERIAL PRIMARY KEY,
 user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 action TEXT NOT NULL,
 request_count BIGINT NOT NULL,
 request_limit BIGINT NOT NULL,
 created BIGINT NOT NULL,
 blocked_until BIGINT NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS anti_theft_events_created ON anti_theft_events(created DESC,id DESC);
`

type Settings struct {
	ProtectCloudPlayback bool
	RateLimitEnabled     bool
	RequestsPerMinute    int
	Action               string
	TemporaryBanMinutes  int
}

func Defaults() Settings {
	return Settings{ProtectCloudPlayback: true, RequestsPerMinute: 60, Action: "warn", TemporaryBanMinutes: 30}
}

func Validate(c Settings) error {
	if c.RequestsPerMinute < 1 || c.RequestsPerMinute > 10000 {
		return errors.New("每分钟请求上限范围为 1–10000")
	}
	if c.Action != "warn" && c.Action != "temporary" && c.Action != "permanent" {
		return errors.New("超限处理必须为警告、临时封禁或永久封禁")
	}
	if c.TemporaryBanMinutes < 1 || c.TemporaryBanMinutes > 43200 {
		return errors.New("临时封禁时长范围为 1–43200 分钟")
	}
	return nil
}

// Legacy explicit opt-outs survive the move to a separate module. Once saved,
// the independent settings are authoritative; unrelated playback saves cannot
// silently enable or disable protection.
func Load(ctx context.Context, db *sql.DB) (Settings, error) {
	c := Defaults()
	var raw string
	err := db.QueryRowContext(ctx, "SELECT v FROM settings WHERE k='feature:anti-theft'").Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		err = db.QueryRowContext(ctx, "SELECT v FROM settings WHERE k='feature:playback'").Scan(&raw)
		if errors.Is(err, sql.ErrNoRows) {
			return c, nil
		}
		if err != nil {
			return c, err
		}
		legacy := struct{ ProtectCloudPlayback bool }{true}
		if err = json.Unmarshal([]byte(raw), &legacy); err != nil {
			return c, err
		}
		c.ProtectCloudPlayback = legacy.ProtectCloudPlayback
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if err = json.Unmarshal([]byte(raw), &c); err != nil {
		return c, err
	}
	return c, Validate(c)
}

type Decision struct {
	Warning      bool
	Permanent    bool
	BlockedUntil int64
}

func (d Decision) Banned(now time.Time) bool { return d.Permanent || d.BlockedUntil > now.UnixMilli() }
func (d Decision) RetrySeconds(now time.Time) int64 {
	return max(1, (d.BlockedUntil-now.UnixMilli()+999)/1000)
}
func (d Decision) Message() string {
	if d.Permanent {
		return "账号因播放请求超限已被永久封禁，请联系管理员"
	}
	return "账号因播放请求超限已被临时封禁，请稍后重试或联系管理员"
}

func Ban(ctx context.Context, db *sql.DB, userID string) (Decision, error) {
	var d Decision
	err := db.QueryRowContext(ctx, `SELECT a.permanent,a.blocked_until FROM anti_theft_accounts a
	 JOIN users u ON u.id=a.user_id WHERE a.user_id=$1 AND u.admin=0`, userID).Scan(&d.Permanent, &d.BlockedUntil)
	if errors.Is(err, sql.ErrNoRows) {
		return d, nil
	}
	return d, err
}

// Count each external GET/HEAD playback entry once. All devices and gateway
// processes share an account lock and a rolling 60-second window in PostgreSQL.
// Warning-only mode keeps the newest limit+1 timestamps, bounding storage while
// preserving whether the rolling window exceeds the limit.
func Admit(ctx context.Context, db *sql.DB, userID string, count bool, now time.Time) (Decision, error) {
	d, err := Ban(ctx, db, userID)
	if err != nil || d.Banned(now) || !count {
		return d, err
	}
	c, err := Load(ctx, db)
	if err != nil || !c.RateLimitEnabled {
		return d, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return d, err
	}
	defer tx.Rollback()
	var admin int
	if err = tx.QueryRowContext(ctx, "SELECT admin FROM users WHERE id=$1 FOR UPDATE", userID).Scan(&admin); err != nil {
		return d, err
	}
	if admin != 0 {
		return Decision{}, nil
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO anti_theft_accounts(user_id) VALUES($1) ON CONFLICT DO NOTHING", userID); err != nil {
		return d, err
	}
	var override sql.NullInt64
	var requests pq.Int64Array
	var warned int64
	if err = tx.QueryRowContext(ctx, "SELECT request_limit,requests,warned_at,permanent,blocked_until FROM anti_theft_accounts WHERE user_id=$1 FOR UPDATE", userID).Scan(&override, &requests, &warned, &d.Permanent, &d.BlockedUntil); err != nil {
		return d, err
	}
	// Recheck after acquiring the lock: another request may have just banned it.
	if d.Banned(now) {
		return d, nil
	}
	limit := c.RequestsPerMinute
	if override.Valid {
		limit = int(override.Int64)
	}
	if limit < 0 || limit > 10000 {
		return Decision{}, errors.New("账号请求上限配置无效")
	}
	if limit == 0 {
		return Decision{}, nil
	}
	stamp := now.UnixMilli()
	recent := make(pq.Int64Array, 0, min(len(requests)+1, limit+1))
	for _, at := range requests {
		if at > stamp-60000 {
			recent = append(recent, at)
		}
	}
	recent = append(recent, stamp)
	if len(recent) > limit+1 {
		recent = recent[len(recent)-limit-1:]
	}
	if len(recent) > limit {
		d.Warning = c.Action == "warn"
		if c.Action == "temporary" {
			d.BlockedUntil = stamp + int64(c.TemporaryBanMinutes)*60000
		}
		if c.Action == "permanent" {
			d.Permanent = true
		}
		if c.Action != "warn" || warned == 0 || stamp-warned >= 60000 {
			if err = record(ctx, tx, userID, c.Action, len(recent), limit, stamp, d.BlockedUntil); err != nil {
				return Decision{}, err
			}
			warned = stamp
		}
	}
	if _, err = tx.ExecContext(ctx, "UPDATE anti_theft_accounts SET requests=$2,warned_at=$3,permanent=$4,blocked_until=$5 WHERE user_id=$1", userID, recent, warned, d.Permanent, d.BlockedUntil); err != nil {
		return Decision{}, err
	}
	if d.Banned(now) {
		if _, err = tx.ExecContext(ctx, "DELETE FROM plays WHERE user_id=$1", userID); err != nil {
			return Decision{}, err
		}
	}
	return d, tx.Commit()
}

func record(ctx context.Context, tx *sql.Tx, userID, action string, count, limit int, stamp, until int64) error {
	if _, err := tx.ExecContext(ctx, "INSERT INTO anti_theft_events(user_id,action,request_count,request_limit,created,blocked_until) VALUES($1,$2,$3,$4,$5,$6)", userID, action, count, limit, stamp, until); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, "DELETE FROM anti_theft_events WHERE id IN (SELECT id FROM anti_theft_events ORDER BY created DESC,id DESC OFFSET 1000)")
	return err
}

func UpdateAccount(ctx context.Context, db *sql.DB, userID string, limit *int, unban bool, now time.Time) error {
	if limit != nil && (*limit < 0 || *limit > 10000) {
		return errors.New("单账号上限范围为 0–10000，留空使用全局上限")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var admin int
	if err = tx.QueryRowContext(ctx, "SELECT admin FROM users WHERE id=$1 FOR UPDATE", userID).Scan(&admin); err != nil {
		return fmt.Errorf("读取账号失败: %w", err)
	}
	if !unban && admin != 0 {
		return errors.New("管理员不参与自动频率限制")
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO anti_theft_accounts(user_id) VALUES($1) ON CONFLICT DO NOTHING", userID); err != nil {
		return err
	}
	if unban {
		if _, err = tx.ExecContext(ctx, "UPDATE anti_theft_accounts SET requests='{}',warned_at=0,permanent=false,blocked_until=0 WHERE user_id=$1", userID); err != nil {
			return err
		}
		if err = record(ctx, tx, userID, "unban", 0, 0, now.UnixMilli(), 0); err != nil {
			return err
		}
	} else {
		if _, err = tx.ExecContext(ctx, "UPDATE anti_theft_accounts SET request_limit=$2,requests='{}',warned_at=0 WHERE user_id=$1", userID, limit); err != nil {
			return err
		}
	}
	return tx.Commit()
}
