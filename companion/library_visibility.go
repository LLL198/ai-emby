package main

// 0x7b9900. Other visibility methods in the original file are still pending.
// Quoted schema identity is captured for the future per-user media CTE.
func (a *App) libraryVisibilitySchema() error {
	if _, err := a.db.Exec("ALTER TABLE libraries ADD COLUMN IF NOT EXISTS hidden BIGINT NOT NULL DEFAULT 0;\n ALTER TABLE users ADD COLUMN IF NOT EXISTS can_view_hidden_libraries BIGINT NOT NULL DEFAULT 0;"); err != nil {
		return err
	}
	return a.db.QueryRow("SELECT quote_ident(current_schema())").Scan(&a.mediaSchema)
}

// 0x7b99c0 fails closed if the user row cannot be read.
func (a *App) canViewHiddenLibraries(userID string) bool {
	var allowed bool
	err := a.db.QueryRow("SELECT can_view_hidden_libraries FROM users WHERE id=?", userID).Scan(&allowed)
	return err == nil && allowed
}
