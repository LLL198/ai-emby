package main

import (
	"log"
	"time"
)

// Recovered from 0x836120 and closure 0x836240. The SQL is preserved exactly
// in schema_recovery.go; this file remains partial until randomPage and
// premiereDate have been reconstructed.
func (a *App) sortSchema() error {
	if _, err := a.db.Exec(recoveredSortSchemaSQL); err != nil {
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

// 0x837c80 updates batches of 500; RowsAffected and database errors both
// terminate the loop, with 50 ms between nonempty batches.
func (a *App) backfillSortFields() error {
	for {
		result, err := a.db.Exec(recoveredSortBackfillSQL)
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
