package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

type fanartExternalIDs struct {
	TVDBID int `json:"tvdb_id"`
}

func (a *App) scraperFanart(ctx context.Context, tmdbPath, artwork string, item Item, tmdbID int, apiConfig tmdbConfig) ([]byte, error) {
	config := a.scraperSettings()
	if ctx != nil {
		if scoped, ok := ctx.Value(scraperPreferencesKey{}).(scraperConfig); ok {
			config = scoped
		}
	}
	apiKey := strings.TrimSpace(config.FanartAPIKey)
	if apiKey == "" {
		return nil, errors.New("光盘图和横幅图需要配置 Fanart.tv API Key")
	}

	mediaType, category := fanartCategory(item.Kind, artwork)
	if category == "" {
		return nil, nil
	}

	providerID := ""
	if mediaType == "tv" {
		basePath := strings.SplitN(tmdbPath, "/season/", 2)[0]
		var externalIDs fanartExternalIDs
		if err := a.tmdbGet(ctx, basePath+"/external_ids", nil, &externalIDs, apiConfig); err != nil {
			return nil, err
		}
		if externalIDs.TVDBID < 1 {
			return nil, nil
		}
		providerID = strconv.FormatInt(int64(externalIDs.TVDBID), 10)
	} else {
		if tmdbID < 1 {
			return nil, nil
		}
		providerID = strconv.FormatInt(int64(tmdbID), 10)
	}

	endpoint := "https://webservice.fanart.tv/v3/" + mediaType + "/" + providerID
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, errors.New("Fanart.tv 请求无效")
	}
	request.Header.Set("api-key", apiKey)
	response, err := a.externalHTTPClient("tmdb", http.DefaultClient).Do(request)
	if err != nil {
		return nil, errors.New("Fanart.tv 请求失败")
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Fanart.tv HTTP %d", response.StatusCode)
	}

	var payload map[string]json.RawMessage
	if err := json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(&payload); err != nil {
		return nil, errors.New("Fanart.tv 响应无效")
	}
	encodedImages, exists := payload[category]
	if !exists {
		return nil, nil
	}
	var images []scraperFanartImage
	if err := json.Unmarshal(encodedImages, &images); err != nil {
		return nil, errors.New("Fanart.tv 图片列表无效")
	}

	languages := []string{"zh", "00", "en", ""}
	if config.OriginalPosters {
		languages = []string{"00", "en", ""}
	}
	season := strconv.Itoa(item.Season)
	for _, language := range languages {
		for _, image := range images {
			if item.Kind == "Season" && image.Season != season {
				continue
			}
			if language != "" && image.Lang != language {
				continue
			}
			parsed, err := url.Parse(image.URL)
			if err != nil || parsed == nil || parsed.User != nil || parsed.Host != "assets.fanart.tv" {
				continue
			}
			if parsed.Scheme != "http" && parsed.Scheme != "https" {
				continue
			}
			if !strings.HasPrefix(parsed.Path, "/fanart/") {
				continue
			}
			parsed.Scheme = "https"
			return a.scraperDownloadArtwork(ctx, parsed.String(), apiConfig.Directory)
		}
	}
	return nil, nil
}

func fanartCategory(kind, artwork string) (mediaType, category string) {
	if kind == "Series" {
		switch artwork {
		case "Poster":
			return "tv", "tvposter"
		case "Backdrop":
			return "tv", "showbackground"
		case "Logo":
			return "tv", "hdtvlogo"
		case "Banner":
			return "tv", "tvbanner"
		}
		return "tv", ""
	}
	if kind == "Season" {
		switch artwork {
		case "Poster":
			return "tv", "seasonposter"
		case "Backdrop":
			return "tv", "seasonbackground"
		case "Banner":
			return "tv", "seasonbanner"
		}
		return "tv", ""
	}
	switch artwork {
	case "Poster":
		return "movies", "movieposter"
	case "Backdrop":
		return "movies", "moviebackground"
	case "Logo":
		return "movies", "hdmovielogo"
	case "Banner":
		return "movies", "moviebanner"
	case "Disc":
		return "movies", "moviedisc"
	default:
		return "movies", ""
	}
}
