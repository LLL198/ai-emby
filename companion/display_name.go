package main

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// Patterns recovered from main.init (0x7657be and 0x7657f2).
var displaySuffixPattern = regexp.MustCompile(`(?i)(?:\((?:19[0-9]{2}|20[0-9]{2}|tmdb(?:id)?-[0-9]+|imdb-tt[0-9]+)\)|（(?:19[0-9]{2}|20[0-9]{2}|tmdb(?:id)?-[0-9]+|imdb-tt[0-9]+)）|\{(?:tmdb(?:id)?-[0-9]+|imdb-tt[0-9]+)\}|\[(?:tmdb(?:id)?-[0-9]+|imdb-tt[0-9]+)\])$`)
var episodeReleasePattern = regexp.MustCompile(`(?i)(?:\bS[0-9]{1,2}[ ._-]*EP?[0-9]{1,3}\b|(?:^|[ ._-])(?:2160p|1080p|720p|WEB-DL|BluRay|H[ .]?26[45]|x26[45])(?:$|[ ._-]))`)

// 0x792ec0. Strip recognized suffixes from right to left, retaining the
// rightmost year/provider ID and never stripping the entire title.
func splitDisplaySuffix(title string) (string, int, M) {
	title = strings.TrimSpace(title)
	year := 0
	ids := M{}
	for {
		match := displaySuffixPattern.FindStringIndex(title)
		if match == nil {
			break
		}
		remaining := strings.TrimSpace(title[:match[0]])
		if remaining == "" {
			break
		}
		suffix := strings.ToLower(strings.Trim(title[match[0]:], "()（）{}[]"))
		switch {
		case strings.HasPrefix(suffix, "tmdb"):
			if ids["Tmdb"] == nil {
				ids["Tmdb"] = strings.SplitN(suffix, "-", 2)[1]
			}
		case strings.HasPrefix(suffix, "imdb-"):
			if ids["Imdb"] == nil {
				ids["Imdb"] = strings.TrimPrefix(suffix, "imdb-")
			}
		default:
			if year == 0 {
				year, _ = strconv.Atoi(suffix)
			}
		}
		title = remaining
	}
	return title, year, ids
}

// 0x7932e0.
func localDisplayFallback(item Item) (string, int, M) {
	title, year, ids := splitDisplaySuffix(item.Name)
	if item.Kind != "Movie" && item.Kind != "Series" {
		return item.Name, 0, M{}
	}
	base := filepath.Base(item.Path)
	candidates := []string{base}
	if item.Kind == "Movie" {
		candidates = []string{strings.TrimSuffix(base, filepath.Ext(base)), filepath.Base(filepath.Dir(item.Path))}
	}
	for _, candidate := range candidates {
		candidateTitle, candidateYear, candidateIDs := splitDisplaySuffix(candidate)
		if item.Kind == "Movie" && candidateYear != 0 && title == candidateTitle+"."+strconv.Itoa(candidateYear) {
			title = candidateTitle
		}
		if year == 0 {
			year = candidateYear
		}
		for key, value := range candidateIDs {
			if ids[key] == nil {
				ids[key] = value
			}
		}
	}
	return title, year, ids
}

// containsDisplayHan names an inlined predicate in the binary.
func containsDisplayHan(title string) bool {
	return strings.IndexFunc(title, func(r rune) bool { return unicode.Is(unicode.Han, r) }) >= 0
}

// 0x7938c0.
func episodeDisplayName(item Item, nfo sidecar, tmdb tmdbData) string {
	tmdbTitle := ""
	if tmdb.ID != 0 {
		tmdbTitle = tmdb.Name
	}
	for _, title := range []string{nfo.Title, tmdbTitle} {
		if containsDisplayHan(title) && !episodeReleaseTitle(title) {
			return strings.TrimSpace(title)
		}
	}
	return "第" + chineseEpisodeNumber(max(item.Episode, 0)) + "集"
}

// 0x793a80.
func episodeReleaseTitle(title string) bool {
	title = strings.TrimSpace(title)
	if episodeReleasePattern.MatchString(title) {
		return true
	}
	title = strings.ToLower(title)
	for _, ext := range []string{".strm", ".mkv", ".mp4", ".avi", ".ts"} {
		if strings.HasSuffix(title, ext) {
			return true
		}
	}
	return false
}

// 0x793c80. The caller clamps episode numbers to zero before calling.
func chineseEpisodeNumber(n int) string {
	digits := []rune("零一二三四五六七八九")
	if n < 10 {
		return string(digits[n])
	}
	for _, unit := range []struct {
		value int
		name  string
	}{{10000, "万"}, {1000, "千"}, {100, "百"}, {10, "十"}} {
		if n < unit.value {
			continue
		}
		quotient, remainder := n/unit.value, n%unit.value
		title := chineseEpisodeNumber(quotient) + unit.name
		if unit.value == 10 && quotient == 1 {
			title = unit.name
		}
		if remainder != 0 {
			if remainder < unit.value/10 {
				title += "零"
			}
			if remainder >= 10 && remainder < 20 && unit.value > 10 {
				title += "一"
			}
			title += chineseEpisodeNumber(remainder)
		}
		return title
	}
	return strconv.Itoa(n)
}

// 0x793f40.
func displayName(item Item, nfo sidecar, tmdb tmdbData) string {
	if item.Kind == "Episode" {
		return episodeDisplayName(item, nfo, tmdb)
	}
	tmdbTitle := ""
	if tmdb.ID != 0 {
		switch item.Kind {
		case "Movie":
			tmdbTitle = tmdb.Title
		case "Series":
			tmdbTitle = tmdb.Name
		}
	}
	if item.Kind == "Movie" || item.Kind == "Series" {
		for _, title := range []string{tmdbTitle, nfo.Title, item.SortName} {
			if containsDisplayHan(title) {
				return strings.TrimSpace(title)
			}
		}
		original := tmdb.OriginalTitle
		if item.Kind == "Series" {
			original = tmdb.OriginalName
		}
		if original = strings.TrimSpace(original); original != "" {
			return original
		}
	}
	if title := strings.TrimSpace(tmdbTitle); title != "" {
		return title
	}
	if title := strings.TrimSpace(nfo.Title); title != "" {
		return title
	}
	if (item.Kind == "Movie" || item.Kind == "Series") && containsDisplayHan(item.SortName) {
		return strings.TrimSpace(item.SortName)
	}
	title, _, _ := localDisplayFallback(item)
	return title
}

// 0x794520.
func (a *App) applyDisplayName(item Item, dto M, nfo sidecar, tmdb tmdbData) {
	name := displayName(item, nfo, tmdb)
	dto["Name"], dto["SortName"] = name, name
	if item.Kind == "Season" {
		dto["IndexNumber"] = item.Season
	}
	_, year, fallbackIDs := localDisplayFallback(item)
	if item.Year == 0 && year != 0 {
		dto["ProductionYear"] = year
	}
	ids, ok := dto["ProviderIds"].(M)
	if !ok || ids == nil {
		ids = M{}
		if nfo.TMDB != "" {
			ids["Tmdb"] = nfo.TMDB
		}
		if nfo.IMDB != "" {
			ids["Imdb"] = nfo.IMDB
		}
		for _, id := range nfo.UniqueIDs {
			switch strings.ToLower(id.Type) {
			case "tmdb":
				ids["Tmdb"] = id.Value
			case "imdb":
				ids["Imdb"] = id.Value
			}
		}
	}
	for key, value := range fallbackIDs {
		if ids[key] == nil {
			ids[key] = value
		}
	}
	if ids["Tmdb"] == nil && tmdb.ID != 0 {
		ids["Tmdb"] = strconv.Itoa(tmdb.ID)
	}
	dto["ProviderIds"] = ids
}
