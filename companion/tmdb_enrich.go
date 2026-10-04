package main

import "strconv"

func (a *App) enrichTMDB(item Item, dto M, metadata sidecar) {
	remote := a.cachedTMDB(item)
	a.applyDisplayName(item, dto, metadata, remote)
	if remote.ID == 0 {
		return
	}
	if overview, ok := dto["Overview"].(string); ok && overview == "" {
		dto["Overview"] = remote.Overview
	}
	if title, ok := dto["OriginalTitle"].(string); ok && title == "" {
		title = remote.OriginalTitle
		if title == "" {
			title = remote.OriginalName
		}
		dto["OriginalTitle"] = title
	}
	if _, exists := dto["CommunityRating"]; !exists && remote.Rating > 0 {
		dto["CommunityRating"] = remote.Rating
	}
	if genres, ok := dto["Genres"].([]string); ok && len(genres) == 0 {
		for _, genre := range remote.Genres {
			genres = append(genres, genre.Name)
		}
		dto["Genres"] = genres
	}
	if _, exists := dto["PremiereDate"]; !exists {
		date := remote.ReleaseDate
		if date == "" {
			date = remote.FirstAirDate
		}
		if date == "" {
			date = remote.AirDate
		}
		if date != "" {
			dto["PremiereDate"] = date + "T00:00:00.0000000Z"
		}
	}
	if providers, ok := dto["ProviderIds"].(M); ok {
		if _, exists := providers["Tmdb"]; !exists {
			providers["Tmdb"] = strconv.Itoa(remote.ID)
		}
	}
}
