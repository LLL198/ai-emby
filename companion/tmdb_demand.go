package main

import (
	"net/http"
	"path/filepath"
	"strings"
)

func tmdbMediaKind(kind string) bool {
	switch kind {
	case "Movie", "Series", "Season", "Episode":
		return true
	}
	return false
}

func (a *App) tmdbImageEligible(item Item, kind string) bool {
	if kind != "Primary" && kind != "Backdrop" && kind != "Thumb" {
		return false
	}
	return tmdbMediaKind(item.Kind) && a.db != nil && a.tmdbSettings().Enabled
}

func (a *App) tmdbImageEligibleForList(item Item, kind string, r *http.Request) bool {
	if kind != "Primary" || !tmdbMediaKind(item.Kind) {
		return false
	}
	if batch := catalogBatchFrom(r); batch != nil {
		return batch.tmdb.Enabled
	}
	return a.tmdbImageEligible(item, kind)
}

func (a *App) hasOwnLocalImage(item Item, kind string) bool {
	if kind == "Primary" {
		var exists int
		if a.db.QueryRow("SELECT 1 FROM covers WHERE id=?", item.ID).Scan(&exists) == nil {
			return true
		}
		switch item.Kind {
		case "Episode":
			return episodeThumb(item) != ""
		case "Season":
			return safeImage(seasonPoster(item.Path, item.Season)) != ""
		}
		if safeImage(item.Poster) != "" {
			return true
		}
		if item.Kind == "Series" && safeImage(seriesFallbackPoster(item.Path)) != "" {
			return true
		}
	}
	directory := filepath.Dir(item.Path)
	if item.Kind == "Series" || item.Kind == "Season" {
		directory = item.Path
	}
	base := strings.TrimSuffix(item.Path, filepath.Ext(item.Path))
	names := map[string][]string{
		"Primary": {"poster", "folder"}, "Backdrop": {"backdrop", "fanart"}, "Thumb": {"thumb", "landscape"},
	}[kind]
	for _, name := range names {
		for _, extension := range []string{".jpg", ".png", ".webp", ".jpeg"} {
			if safeImage(base+"-"+name+extension) != "" || safeImage(filepath.Join(directory, name+extension)) != "" {
				return true
			}
		}
	}
	return false
}

func (a *App) needsTMDBDetail(item Item) bool {
	if !a.tmdbImageEligible(item, "Primary") {
		return false
	}
	if item.Kind == "Movie" || item.Kind == "Series" {
		return true
	}
	metadata := a.metadata(item)
	if item.Kind == "Episode" && !containsDisplayHan(metadata.Title) {
		return true
	}
	if item.Overview == "" && metadata.Plot == "" {
		return true
	}
	return !a.hasOwnLocalImage(item, "Primary")
}

func (a *App) cachedTMDBImage(item Item, kind string) string {
	data := a.cachedTMDB(item)
	if kind == "Primary" && data.Poster == "" && data.Still == "" {
		settings := a.tmdbSettings()
		if settings.Enabled {
			key, _ := a.tmdbIdentity(item)
			if key != "" {
				if record, ok := readTMDB(key+"|primary", settings.Directory); ok && record.Until == 0 {
					data = record.Data
				}
			}
		}
	}
	switch kind {
	case "Primary":
		if item.Kind == "Episode" {
			return tmdbArtwork(data.Still)
		}
		return tmdbArtwork(data.Poster)
	case "Backdrop":
		return tmdbArtwork(data.Backdrop)
	case "Thumb":
		return tmdbArtwork(data.Still)
	}
	return ""
}
