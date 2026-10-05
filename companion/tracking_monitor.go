package main

import (
	"path/filepath"
	"strings"
)

func (a *App) trackingScrapeExclusions() ([]string, error) {
	rows, err := a.db.Query("SELECT data::jsonb->>'Output' FROM feature_tracking_imports WHERE data::jsonb->>'SkipAutoScrape'='true' AND coalesce(data::jsonb->>'Output','')<>''")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	paths := []string{}
	for rows.Next() {
		var output string
		if err := rows.Scan(&output); err != nil {
			return nil, err
		}
		if _, err := cloudOutput(output); err == nil {
			paths = append(paths, filepath.Clean(output))
		}
	}
	return paths, rows.Err()
}

func trackingScrapeExcluded(path string, exclusions []string) bool {
	path = filepath.Clean(path)
	for _, output := range exclusions {
		if path == output || strings.HasPrefix(path, output+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func cloudOutputReserved(path string) bool {
	cloudManageMu.Lock()
	defer cloudManageMu.Unlock()
	for _, output := range cloudOutputs {
		if pathsOverlap(path, output) {
			return true
		}
	}
	return false
}
