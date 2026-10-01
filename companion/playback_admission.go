package main

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"net/http"
	"time"
)

// Reconstructed from the exact executable's Ghidra output and its embedded
// SQL/error strings. It was absent from the local reference source snapshot.
type playbackDenied string

func (e playbackDenied) Error() string { return string(e) }

func playbackAdmissionError(w http.ResponseWriter, r *http.Request, err error) {
	var denied playbackDenied
	if errors.As(err, &denied) {
		fail(w, http.StatusForbidden, denied.Error())
		return
	}
	log.Printf("playback admission unavailable: %v", err)
	fail(w, http.StatusServiceUnavailable, "播放校验暂时不可用，请重试")
}

func (a *App) playbackPermission(parent context.Context, userID string) error {
	ctx, cancel := context.WithTimeout(parent, time.Second)
	defer cancel()

	var allowed bool
	err := a.db.QueryRowContext(ctx,
		"SELECT allowed FROM user_playback WHERE user_id=$1", userID,
	).Scan(&allowed)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if !allowed {
		return playbackDenied("管理员已禁止该用户播放")
	}
	return nil
}

func (a *App) reserveContext(parent context.Context, u User, item string) error {
	if u.API {
		return playbackDenied("播放需要用户令牌")
	}
	ctx, cancel := context.WithTimeout(parent, time.Second)
	defer cancel()
	if err := a.playbackPermission(ctx, u.ID); err != nil {
		return err
	}

	now := time.Now().Unix()
	result, err := a.db.ExecContext(ctx,
		"UPDATE plays SET item=$1,updated=$2 WHERE user_id=$3 AND device=$4 AND updated>=$5",
		item, now, u.ID, u.Device, now-180,
	)
	if err != nil {
		return err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if updated > 0 {
		return nil
	}

	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var maxDevices int
	if err := tx.QueryRowContext(ctx,
		"SELECT max_devices FROM users WHERE id=$1 FOR UPDATE", u.ID,
	).Scan(&maxDevices); err != nil {
		return err
	}

	now = time.Now().Unix()
	var otherDevices, currentDevice int
	err = tx.QueryRowContext(ctx,
		"SELECT count(*) FILTER (WHERE device<>$2), count(*) FILTER (WHERE device=$2) FROM plays WHERE user_id=$1 AND updated>=$3",
		u.ID, u.Device, now-180,
	).Scan(&otherDevices, &currentDevice)
	if err != nil {
		return err
	}
	if currentDevice == 0 && otherDevices >= maxDevices {
		return playbackDenied("已达到同时播放设备上限")
	}

	if _, err := tx.ExecContext(ctx,
		"DELETE FROM plays WHERE user_id=$1 AND updated<$2", u.ID, now-180,
	); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO plays VALUES($1,$2,$3,$4) ON CONFLICT(user_id,device) DO UPDATE SET item=excluded.item,updated=excluded.updated",
		u.ID, u.Device, item, now,
	); err != nil {
		return err
	}
	return tx.Commit()
}
