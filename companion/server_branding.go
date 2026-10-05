package main

func (a *App) migrateServerName() error {
	tx, err := a.db.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec("INSERT INTO settings(k,v) VALUES('server_name','AI Emby') ON CONFLICT(k) DO NOTHING"); err != nil {
		return err
	}
	if _, err = tx.Exec("UPDATE settings SET v='AI Emby' WHERE k='server_name' AND lower(trim(v)) IN ('go emby','go-emby','goemby','maca','ai emby','ai-emby','aiemby','') AND NOT EXISTS(SELECT 1 FROM settings WHERE k='feature:server-branding-v2')"); err != nil {
		return err
	}
	if _, err = tx.Exec("INSERT INTO settings(k,v) VALUES('feature:server-branding-v2','true') ON CONFLICT(k) DO NOTHING"); err != nil {
		return err
	}
	return tx.Commit()
}
