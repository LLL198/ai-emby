package main

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
)

type tmdbScraper struct{ fanart bool }

type scraperTMDBCandidate struct {
	ID            int
	Title         string
	Name          string
	OriginalTitle string `json:"original_title"`
	OriginalName  string `json:"original_name"`
	ReleaseDate   string `json:"release_date"`
	FirstAirDate  string `json:"first_air_date"`
	MediaType     string `json:"media_type"`
}

type tmdbSearchResponse struct {
	Results    []scraperTMDBCandidate `json:"results"`
	TotalPages int                    `json:"total_pages"`
}

var errTMDBNoArtwork = errors.New("无可用图片")
var errTMDBEpisodeNotFound = errors.New("TMDB 单集不存在，需核对分集编号")

func (tmdbScraper) Name() string { return "TMDB" }

func init() { RegisterScraper(tmdbScraper{}) }

func (a *App) scraperDownloadArtwork(ctx context.Context, rawURL, cacheDirectory string) ([]byte, error) {
	release, ok := lockTMDBImage(ctx, rawURL)
	if !ok {
		return nil, errors.New("图片请求已取消")
	}
	defer release()

	cachePath := filepath.Join(cacheDirectory, "images", digest(rawURL)+".img")
	if data, ok := readArtworkCache(cachePath); ok {
		return data, nil
	}

	data, err := a.downloadScraperArtwork(ctx, rawURL)
	if err != nil {
		return nil, err
	}
	writeArtworkCache(cachePath, data)
	return data, nil
}

// Digits are part of the title: stripping them broadens searches to other
// sequels and can reject an exact localized-title match.
func scraperTitleVariants(title string) []string {
	return []string{title}
}

func scraperSameTitleNumbers(title, localized, original string) bool {
	numbers := func(s string) string { return strings.Join(namingTitleNumbers.FindAllString(s, -1), ",") }
	return numbers(title) == numbers(localized) || original != "" && numbers(title) == numbers(original)
}

func scraperTitleKey(title string) string {
	return strings.Map(func(r rune) rune {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return -1
		}
		return unicode.ToLower(r)
	}, title)
}

func scraperCandidateScore(mediaType, title string, year, season, episode int, candidate scraperTMDBCandidate) int {
	if candidate.ID < 1 || (candidate.MediaType != "" && candidate.MediaType != mediaType) {
		return 0
	}
	if (season > 0 || episode > 0) && mediaType != "tv" {
		return 0
	}

	localizedTitle, originalTitle, date := candidate.Title, candidate.OriginalTitle, candidate.ReleaseDate
	if mediaType == "tv" {
		localizedTitle, originalTitle, date = candidate.Name, candidate.OriginalName, candidate.FirstAirDate
	}
	variants := scraperTitleVariants(title)
	localizedKey := scraperTitleKey(localizedTitle)
	originalKey := scraperTitleKey(originalTitle)

	bestTitleScore := 0
	for _, variant := range variants {
		key := scraperTitleKey(variant)
		variantScore := 0
		if key != "" && key == localizedKey {
			variantScore = 70
		}
		if key != "" && key == originalKey {
			variantScore = 75
		}
		if variant == title && variantScore > 0 {
			variantScore += 5
		}
		if variantScore > bestTitleScore {
			bestTitleScore = variantScore
		}
	}
	if bestTitleScore == 0 {
		return 0
	}
	if year > 0 {
		if len(date) < 4 {
			return 0
		}
		candidateYear, err := strconv.Atoi(date[:4])
		if err != nil || candidateYear != year {
			return 0
		}
		bestTitleScore += 15
	}
	if mediaType == "tv" && (season > 0 || episode > 0) {
		return bestTitleScore + 15
	}
	return bestTitleScore + 10
}

func scraperSelectCandidate(mediaType string, candidates []scraperTMDBCandidate, title string, year, season, episode int) (scraperTMDBCandidate, error) {
	highConfidence := make(map[int]int)
	for _, candidate := range candidates {
		if score := scraperCandidateScore(mediaType, title, year, season, episode, candidate); score > 79 {
			highConfidence[candidate.ID] = score
		}
	}
	if len(highConfidence) == 1 {
		for id := range highConfidence {
			for _, candidate := range candidates {
				if candidate.ID == id {
					return candidate, nil
				}
			}
		}
		return scraperTMDBCandidate{}, errors.New("TMDB 候选状态无效")
	}
	if len(highConfidence) == 0 && year > 0 {
		// TMDB may find a localized alias that differs from the canonical and
		// original titles returned by search (for example 海贼王 / 航海王).
		// Search was already constrained by media type and year; accept only a
		// single candidate whose returned release year confirms that filter.
		yearMatches := make(map[int]scraperTMDBCandidate)
		for _, candidate := range candidates {
			if candidate.ID < 1 || (candidate.MediaType != "" && candidate.MediaType != mediaType) {
				continue
			}
			if (season > 0 || episode > 0) && mediaType != "tv" {
				continue
			}
			localized, original := candidate.Title, candidate.OriginalTitle
			if mediaType == "tv" {
				localized, original = candidate.Name, candidate.OriginalName
			}
			if !scraperSameTitleNumbers(title, localized, original) {
				continue
			}
			date := candidate.ReleaseDate
			if mediaType == "tv" {
				date = candidate.FirstAirDate
			}
			if len(date) < 4 {
				continue
			}
			candidateYear, err := strconv.Atoi(date[:4])
			if err == nil && candidateYear == year {
				yearMatches[candidate.ID] = candidate
			}
		}
		if len(yearMatches) == 1 {
			for _, candidate := range yearMatches {
				return candidate, nil
			}
		}
	}
	if len(highConfidence) != 1 {
		return scraperTMDBCandidate{}, fmt.Errorf(
			"识别失败：标题 %s，年份 %d，高可信候选数 %d；低可信、年份/类型冲突或多候选，跳过",
			title, year, len(highConfidence),
		)
	}
	return scraperTMDBCandidate{}, errors.New("TMDB 候选状态无效")
}

func (a *App) scraperSearchTMDB(ctx context.Context, settings tmdbConfig, mediaType, title string, year, season, episode int) (scraperTMDBCandidate, error) {
	var candidates []scraperTMDBCandidate
	if scraperTitleKey(title) != "" {
		for _, queryTitle := range scraperTitleVariants(title) {
			query := url.Values{"query": []string{queryTitle}}
			if year > 0 {
				yearKey := "year"
				if mediaType == "tv" {
					yearKey = "first_air_date_year"
				}
				query.Set(yearKey, strconv.Itoa(year))
			}

			var response tmdbSearchResponse
			if err := a.tmdbGet(ctx, "search/"+mediaType, query, &response, settings); err != nil {
				return scraperTMDBCandidate{}, fmt.Errorf("识别请求失败：%w", err)
			}
			if response.TotalPages > 1 {
				return scraperTMDBCandidate{}, errors.New("识别失败：搜索候选跨多页，无法确认唯一性，跳过")
			}
			candidates = append(candidates, response.Results...)
		}
	}
	return scraperSelectCandidate(mediaType, candidates, title, year, season, episode)
}

type tmdbNFOUniqueID struct {
	Type    string `xml:"type,attr"`
	Default bool   `xml:"default,attr,omitempty"`
	Value   string `xml:",chardata"`
}

type tmdbNFORecord struct {
	XMLName       xml.Name
	Title         string            `xml:"title,omitempty"`
	OriginalTitle string            `xml:"originaltitle,omitempty"`
	Plot          string            `xml:"plot,omitempty"`
	Year          int               `xml:"year,omitempty"`
	Premiered     string            `xml:"premiered,omitempty"`
	Rating        float64           `xml:"rating,omitempty"`
	TMDBID        string            `xml:"tmdbid,omitempty"`
	IMDBID        string            `xml:"imdbid,omitempty"`
	UniqueIDs     []tmdbNFOUniqueID `xml:"uniqueid,omitempty"`
	Genres        []string          `xml:"genre,omitempty"`
	Season        int               `xml:"season,omitempty"`
	Episode       int               `xml:"episode,omitempty"`
}

func tmdbNFODate(kind string, data tmdbData) string {
	switch kind {
	case "Movie":
		return data.ReleaseDate
	case "Episode":
		return data.AirDate
	default:
		return data.FirstAirDate
	}
}

func tmdbNFOYear(item Item, date string) int {
	if item.Kind != "Movie" && item.Year > 0 {
		return item.Year
	}
	if len(date) >= 4 {
		if year, err := strconv.Atoi(date[:4]); err == nil && year >= 1800 && year <= 2199 {
			return year
		}
	}
	return item.Year
}

func tmdbNFO(item Item, data tmdbData, local sidecar) ([]byte, error) {
	root := "movie"
	switch item.Kind {
	case "Series":
		root = "tvshow"
	case "Season":
		root = "season"
	case "Episode":
		root = "episodedetails"
	}

	title := data.Title
	originalTitle := data.OriginalTitle
	if item.Kind != "Movie" {
		if data.Name != "" {
			title = data.Name
		}
		if data.OriginalName != "" {
			originalTitle = data.OriginalName
		}
	}
	if title == "" {
		title = item.Name
	}
	if originalTitle == "" {
		originalTitle = local.OriginalTitle
	}
	plot := data.Overview
	if plot == "" {
		plot = local.Plot
	}
	if plot == "" {
		plot = item.Overview
	}
	date := tmdbNFODate(item.Kind, data)
	if date == "" {
		date = local.Premiered
	}
	rating := data.Rating
	if rating == 0 {
		rating = local.Rating
	}
	nfo := tmdbNFORecord{
		XMLName:       xml.Name{Local: root},
		Title:         title,
		OriginalTitle: originalTitle,
		Plot:          plot,
		Year:          tmdbNFOYear(item, date),
		Premiered:     date,
		Rating:        rating,
		TMDBID:        strconv.Itoa(data.ID),
		IMDBID:        local.IMDB,
		Season:        item.Season,
		Episode:       item.Episode,
	}
	if nfo.TMDBID == "0" {
		nfo.TMDBID = local.TMDB
	}
	if nfo.TMDBID != "" {
		nfo.UniqueIDs = append(nfo.UniqueIDs, tmdbNFOUniqueID{Type: "tmdb", Default: true, Value: nfo.TMDBID})
	}
	if nfo.IMDBID != "" {
		nfo.UniqueIDs = append(nfo.UniqueIDs, tmdbNFOUniqueID{Type: "imdb", Value: nfo.IMDBID})
	}
	for _, genre := range data.Genres {
		if name := strings.TrimSpace(genre.Name); name != "" {
			nfo.Genres = append(nfo.Genres, name)
		}
	}
	if len(nfo.Genres) == 0 {
		nfo.Genres = append(nfo.Genres, local.Genres...)
	}

	encoded, err := xml.MarshalIndent(nfo, "", "  ")
	if err != nil {
		return nil, err
	}
	return append([]byte(xml.Header), encoded...), nil
}

func (s tmdbScraper) Fetch(ctx context.Context, app *App, item Item, artwork string) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !s.fanart && (artwork == "Disc" || artwork == "Banner") {
		return nil, nil
	}
	if item.Kind != "Movie" && item.Kind != "Series" && item.Kind != "Season" && item.Kind != "Episode" {
		return nil, nil
	}

	settings := app.tmdbSettings()
	item, key, endpoint, err := app.resolveTMDBItem(ctx, item, settings)
	if err != nil {
		return nil, err
	}
	if err := app.ensureTMDBRequest(ctx, item, false, true); err != nil {
		if item.Kind == "Episode" && strings.Contains(err.Error(), "TMDB HTTP 404") {
			return nil, errTMDBEpisodeNotFound
		}
		return nil, err
	}

	key, endpoint = app.tmdbIdentity(item)
	record, ok := readTMDB(key, settings.Directory)
	if !ok || record.Data.ID < 1 {
		return nil, errors.New("TMDB 元数据缓存为空或记录了此前失败；请检查 TMDB 日志中的原始原因后重试")
	}
	data := record.Data
	resolvedEndpoint, err := app.scraperEndpoint(ctx, item, endpoint)
	if err != nil {
		return nil, err
	}
	endpoint = resolvedEndpoint

	if s.fanart {
		return app.scraperFanart(ctx, endpoint, artwork, item, data.ID, settings)
	}
	if item.Kind == "Season" || item.Kind == "Episode" {
		var detail tmdbData
		if err := app.tmdbGet(ctx, endpoint, nil, &detail, settings); err != nil {
			if item.Kind == "Episode" && strings.Contains(err.Error(), "TMDB HTTP 404") {
				return nil, errTMDBEpisodeNotFound
			}
			return nil, err
		}
		if detail.Name != "" {
			data.Name = detail.Name
		}
		if detail.OriginalName != "" {
			data.OriginalName = detail.OriginalName
		}
		if detail.Overview != "" {
			data.Overview = detail.Overview
		}
		if detail.FirstAirDate != "" {
			data.FirstAirDate = detail.FirstAirDate
		}
		if detail.AirDate != "" {
			data.AirDate = detail.AirDate
		}
		if detail.Poster != "" {
			data.Poster = detail.Poster
		}
		data.Still = detail.Still
		if detail.Rating != 0 {
			data.Rating = detail.Rating
		}
	}
	if artwork == "NFO" {
		if err := app.scraperMetadata(ctx, item, endpoint); err != nil {
			return nil, err
		}
		originalKey := "scraper|original-metadata|" + item.ID + "|" + endpoint
		if original, ok := readTMDB(originalKey, settings.Directory); ok {
			if data.OriginalTitle == "" {
				data.OriginalTitle = original.Data.OriginalTitle
			}
			if data.OriginalName == "" {
				data.OriginalName = original.Data.OriginalName
			}
		}
		return tmdbNFO(item, data, app.metadata(item))
	}

	var path string
	switch artwork {
	case "Poster":
		path, err = app.scraperPosterPath(ctx, endpoint, data.Poster)
	case "Backdrop", "Logo":
		originalLanguage, languageErr := app.scraperOriginal(ctx, endpoint)
		if languageErr != nil {
			return nil, languageErr
		}
		languages := scraperImageLanguageOrder(scraperPreferences(ctx), originalLanguage)
		query := url.Values{}
		query.Set("include_image_language", strings.Join(languages, ","))
		var images tmdbImageSet
		if err := app.tmdbGet(ctx, strings.TrimRight(endpoint, "/")+"/images", query, &images, settings); err != nil {
			return nil, err
		}
		if artwork == "Backdrop" {
			path = chooseScraperPoster(images.Backdrops, languages)
			if path == "" {
				path = data.Backdrop
			}
		} else {
			path = chooseScraperPoster(images.Logos, languages)
		}
	case "Still":
		if item.Kind != "Episode" {
			return nil, nil
		}
		path = data.Still
	default:
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if path == "" {
		return nil, errTMDBNoArtwork
	}
	rawURL := tmdbArtwork(path)
	if rawURL == "" {
		return nil, errors.New("TMDB 图片路径无效")
	}
	return app.scraperDownloadArtwork(ctx, rawURL, settings.Directory)
}
