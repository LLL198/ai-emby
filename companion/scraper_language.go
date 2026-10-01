package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func scraperPreferences(ctx context.Context) scraperConfig {
	if ctx != nil {
		if config, ok := ctx.Value(scraperPreferencesKey{}).(scraperConfig); ok {
			return config
		}
	}

	return scraperConfig{
		Scraper: "TMDB",
		Categories: map[string]scraperCategory{
			"Movie": {
				Enabled: true,
				Content: []string{"NFO", "Poster", "Backdrop", "Logo", "Disc", "Banner"},
			},
			"Series": {
				Enabled: true,
				Content: []string{"NFO", "Poster", "Backdrop", "Logo", "Banner"},
			},
			"Season": {
				Enabled: true,
				Content: []string{"NFO", "Poster", "Banner"},
			},
			"Episode": {
				Enabled: true,
				Content: []string{"NFO", "Still"},
			},
		},
	}
}

// scraperEndpoint replaces an episode/season path's unresolved or stale
// series ID with the ID stored in the refreshed TMDB record. The binary's
// error path explicitly reports the Chinese "电视剧 TMDB 编号不可用" message
// when that record cannot supply a numeric ID.
func (a *App) scraperEndpoint(ctx context.Context, item Item, endpoint string) (string, error) {
	if item.Kind != "Season" && item.Kind != "Episode" {
		return endpoint, nil
	}
	if err := a.ensureTMDBRequest(ctx, item, false, true); err != nil {
		return "", err
	}

	key, _ := a.tmdbIdentity(item)
	settings := a.tmdbSettings()
	record, ok := readTMDB(key, settings.Directory)
	if !ok || record.Data.ID < 1 {
		return "", errors.New("电视剧 TMDB 编号不可用")
	}

	suffix := ""
	if index := strings.Index(endpoint, "/season/"); index >= 0 {
		suffix = endpoint[index:]
	} else if item.Kind == "Episode" {
		suffix = fmt.Sprintf("/season/%d/episode/%d", item.Season, item.Episode)
	} else {
		suffix = fmt.Sprintf("/season/%d", item.Season)
	}
	return "tv/" + strconv.Itoa(record.Data.ID) + suffix, nil
}

func tmdbSeriesEndpoint(endpoint string) string {
	for _, marker := range []string{"/season/", "/episode/"} {
		if index := strings.Index(endpoint, marker); index >= 0 {
			endpoint = endpoint[:index]
		}
	}
	return strings.Trim(endpoint, "/")
}

// scraperOriginal obtains the series/movie's original-language tag. Season
// and episode endpoints are reduced to their parent series first, matching
// the path slicing observed in the binary.
func (a *App) scraperOriginal(ctx context.Context, endpoint string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	endpoint = tmdbSeriesEndpoint(endpoint)
	if endpoint == "" || strings.Contains(endpoint, "name:") {
		return "", errors.New("TMDB 元数据不完整")
	}
	var data scraperLocalizedData
	if err := a.tmdbGet(ctx, endpoint, nil, &data, a.tmdbSettings()); err != nil {
		return "", fmt.Errorf("TMDB 原始语言查询失败：%w", err)
	}
	return strings.TrimSpace(data.OriginalLanguage), nil
}

// scraperMetadata caches the source-language metadata used alongside the
// localized TMDB record. The cache namespace is present verbatim in the
// binary; the exact cache lifetime and field merge rules are inferred.
func (a *App) scraperMetadata(ctx context.Context, item Item, endpoint string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	settings := a.tmdbSettings()
	key := "scraper|original-metadata|" + item.ID + "|" + endpoint
	release, ok := lockTMDBImage(ctx, key)
	if !ok {
		return errors.New("图片请求已取消")
	}
	defer release()
	if record, ok := readTMDB(key, settings.Directory); ok && record.Data.ID > 0 {
		return nil
	}

	language, err := a.scraperOriginal(ctx, endpoint)
	if err != nil {
		return err
	}
	if language == "" {
		return nil
	}
	localizedContext := context.WithValue(ctx, scraperLanguageKey{}, language)
	var data scraperLocalizedData
	if err := a.tmdbGet(localizedContext, endpoint, nil, &data, settings); err != nil {
		return fmt.Errorf("TMDB 原始语言元数据请求失败：%w", err)
	}
	if data.ID < 1 {
		if record, ok := readTMDB(tmdbCacheKeyForItem(a, item), settings.Directory); ok {
			data.ID = record.Data.ID
		}
	}
	if data.ID < 1 {
		return errors.New("TMDB 响应缺少编号")
	}
	return writeTMDB(settings.Directory, tmdbRecord{
		Key:   key,
		Until: time.Now().Add(30 * 24 * time.Hour).Unix(),
		Data:  data.tmdbData,
	})
}

// tmdbCacheKeyForItem mirrors the stable cache namespace used by the TMDB
// identity helper and is shared here only to link original-language metadata
// to the already resolved provider record.
func tmdbCacheKeyForItem(app *App, item Item) string {
	key, _ := app.tmdbIdentity(item)
	return key
}

type tmdbImageSet struct {
	Posters   []scraperPoster `json:"posters"`
	Backdrops []scraperPoster `json:"backdrops"`
	Logos     []scraperPoster `json:"logos"`
}

func scraperImageLanguageOrder(config scraperConfig, originalLanguage string) []string {
	var languages []string
	add := func(language string) {
		language = strings.TrimSpace(language)
		if language == "" {
			return
		}
		for _, existing := range languages {
			if existing == language {
				return
			}
		}
		languages = append(languages, language)
	}
	if config.OriginalPosters {
		add(originalLanguage)
	}
	if config.ChineseMetadata {
		add("zh")
		add("zh-CN")
	}
	add("en")
	add(originalLanguage)
	add("null")
	return languages
}

func chooseScraperPoster(images []scraperPoster, languages []string) string {
	for _, language := range languages {
		for _, image := range images {
			if strings.EqualFold(image.Language, language) && image.Path != "" {
				return image.Path
			}
		}
	}
	if len(images) > 0 {
		return images[0].Path
	}
	return ""
}

// scraperPosterPath chooses a TMDB poster in the user's preferred language
// order, falling back to the path in the detail response when the image list
// has no matching locale.
func (a *App) scraperPosterPath(ctx context.Context, endpoint, fallbackPath string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	originalLanguage, err := a.scraperOriginal(ctx, endpoint)
	if err != nil {
		return fallbackPath, err
	}
	config := scraperPreferences(ctx)
	languages := scraperImageLanguageOrder(config, originalLanguage)
	query := url.Values{}
	query.Set("include_image_language", strings.Join(languages, ","))
	var images tmdbImageSet
	if err := a.tmdbGet(ctx, strings.TrimRight(endpoint, "/")+"/images", query, &images, a.tmdbSettings()); err != nil {
		return fallbackPath, err
	}
	if path := chooseScraperPoster(images.Posters, languages); path != "" {
		return path, nil
	}
	return fallbackPath, nil
}
