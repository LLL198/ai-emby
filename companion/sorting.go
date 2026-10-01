package main

import (
	"log"
	"time"
)

func (a *App) sortSchema() error {
	if _, err := a.db.Exec(sortSchemaSQL); err != nil {
		return err
	}
	var version string
	if a.db.QueryRow("SELECT v FROM settings WHERE k='sort_fields_v2'").Scan(&version) == nil {
		return nil
	}
	go func() {
		if err := a.backfillSortFields(); err != nil {
			log.Printf("sort migration: backfill failed: %v", err)
			return
		}
		a.db.Exec("INSERT INTO settings(k,v) VALUES('sort_fields_v2','1') ON CONFLICT (k) DO UPDATE SET v=EXCLUDED.v")
	}()
	return nil
}

// Backfill 500 rows per batch, yielding for 50 ms between batches.
func (a *App) backfillSortFields() error {
	for {
		result, err := a.db.Exec(sortBackfillSQL)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if count == 0 {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
}
