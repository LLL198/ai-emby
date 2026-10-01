package main

func (a *App) libraryVisibilitySchema() error {
	if _, err := a.db.Exec("ALTER TABLE libraries ADD COLUMN IF NOT EXISTS hidden BIGINT NOT NULL DEFAULT 0;\n ALTER TABLE users ADD COLUMN IF NOT EXISTS can_view_hidden_libraries BIGINT NOT NULL DEFAULT 0;"); err != nil {
		return err
	}
	return a.db.QueryRow("SELECT quote_ident(current_schema())").Scan(&a.mediaSchema)
}

// Deny access if the user permission cannot be read.
func (a *App) canViewHiddenLibraries(userID string) bool {
	var allowed bool
	err := a.db.QueryRow("SELECT can_view_hidden_libraries FROM users WHERE id=?", userID).Scan(&allowed)
	return err == nil && allowed
}
