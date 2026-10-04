package main

import (
	"encoding/json"
	"strings"
)

func (a *App) refreshSidecar(item Item) sidecar {
	current := readSidecar(item)
	var stored string
	var previous sidecar
	if a.db.QueryRow("SELECT data FROM item_metadata WHERE item=?", item.ID).Scan(&stored) == nil {
		_ = json.Unmarshal([]byte(stored), &previous)
	}
	return mergeRefreshSidecar(item, current, previous)
}

func mergeRefreshSidecar(item Item, current, previous sidecar) sidecar {
	if strings.TrimSpace(current.Title) == "" || (!containsDisplayHan(current.Title) && containsDisplayHan(previous.Title)) {
		current.Title = previous.Title
	}
	if strings.TrimSpace(current.Plot) == "" {
		current.Plot = previous.Plot
		if current.Plot == "" {
			current.Plot = item.Overview
		}
	}
	if current.TMDB == "" {
		current.TMDB = previous.TMDB
	}
	if current.IMDB == "" {
		current.IMDB = previous.IMDB
	}
	if current.Premiered == "" {
		current.Premiered = previous.Premiered
	}
	if len(current.Actors) == 0 {
		current.Actors = previous.Actors
	}
	if len(current.Directors) == 0 {
		current.Directors = previous.Directors
	}
	if len(current.Genres) == 0 {
		current.Genres = previous.Genres
	}
	if len(current.Studios) == 0 {
		current.Studios = previous.Studios
	}
	return current
}
