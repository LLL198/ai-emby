package main

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

const (
	bangumiAPIBase = "https://api.bgm.tv/v0/"
	bangumiAgent   = "ai-emby/1.0 (media metadata scraper)"
)

type bangumiSubject struct {
	ID       int    `json:"id"`
	Type     int    `json:"type"`
	Name     string `json:"name"`
	Chinese  string `json:"name_cn"`
	Summary  string `json:"summary"`
	Date     string `json:"date"`
	Platform string `json:"platform"`
	Images   struct {
		Large string `json:"large"`
	} `json:"images"`
}

type bangumiSearchResponse struct {
	Data []bangumiSubject `json:"data"`
}

type bangumiNFO struct {
	XMLName   xml.Name
	Title     string `xml:"title,omitempty"`
	Original  string `xml:"originaltitle,omitempty"`
	Plot      string `xml:"plot,omitempty"`
	Premiered string `xml:"premiered,omitempty"`
	Year      int    `xml:"year,omitempty"`
	Bangumi   int    `xml:"bangumiid,omitempty"`
}

func (bangumiScraper) Name() string { return "Bangumi" }

func (a *App) bangumiRequest(ctx context.Context, method, path string, body, out any) error {
	var requestBody io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		requestBody = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, bangumiAPIBase+path, requestBody)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", bangumiAgent)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	client := a.externalHTTPClient("tmdb", http.DefaultClient)
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("Bangumi 请求失败")
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Bangumi HTTP %d", resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(out)
}

func (bangumiScraper) Fetch(ctx context.Context, app *App, item Item, artwork string) ([]byte, error) {
	if artwork != "NFO" && artwork != "Poster" {
		return nil, nil
	}
	if item.Kind != "Movie" && item.Kind != "Series" {
		return nil, fmt.Errorf("Bangumi 暂不自动匹配季和单集条目")
	}

	identity := scraperSearchIdentity(item.Name)
	title := strings.TrimSpace(identity.Title)
	if title == "" {
		return nil, fmt.Errorf("Bangumi 缺少可匹配标题")
	}
	year := identity.Year
	if year == 0 {
		year = item.Year
	}

	request := map[string]any{
		"keyword": title,
		"sort":    "match",
		"filter": map[string]any{
			"type": []int{2},
		},
	}
	var search bangumiSearchResponse
	if err := app.bangumiRequest(ctx, http.MethodPost, "search/subjects?limit=20", request, &search); err != nil {
		return nil, err
	}

	matches := make([]bangumiSubject, 0, 1)
	for _, candidate := range search.Data {
		if candidate.Type != 2 || !bangumiSameTitle(candidate, title) {
			continue
		}
		if year > 0 && !strings.HasPrefix(candidate.Date, strconv.Itoa(year)+"-") {
			continue
		}
		if !bangumiPlatformMatches(candidate.Platform, item.Kind) {
			continue
		}
		matches = append(matches, candidate)
	}
	if len(matches) != 1 {
		return nil, fmt.Errorf("Bangumi 未找到唯一可信的同名、同年份及类型条目")
	}

	var subject bangumiSubject
	if err := app.bangumiRequest(ctx, http.MethodGet, "subjects/"+strconv.Itoa(matches[0].ID), nil, &subject); err != nil {
		return nil, err
	}
	if artwork == "Poster" {
		return app.bangumiPoster(ctx, subject.Images.Large)
	}

	localName := "movie"
	if item.Kind == "Series" {
		localName = "tvshow"
	}
	nfo := bangumiNFO{
		XMLName:   xml.Name{Local: localName},
		Title:     subject.Name,
		Original:  subject.Name,
		Plot:      subject.Summary,
		Premiered: subject.Date,
		Bangumi:   subject.ID,
	}
	preferences := scraperPreferences(ctx)
	if preferences.ChineseMetadata && subject.Chinese != "" {
		nfo.Title = subject.Chinese
	}
	if len(subject.Date) > 3 {
		nfo.Year, _ = strconv.Atoi(subject.Date[:4])
	}

	encoded, err := xml.MarshalIndent(nfo, "", "  ")
	if err != nil {
		return nil, err
	}
	return append([]byte(xml.Header), encoded...), nil
}

func bangumiSameTitle(subject bangumiSubject, title string) bool {
	title = strings.TrimSpace(title)
	return strings.EqualFold(strings.TrimSpace(subject.Chinese), title) ||
		strings.EqualFold(strings.TrimSpace(subject.Name), title)
}

func bangumiPlatformMatches(platform, kind string) bool {
	switch kind {
	case "Movie":
		return platform == "剧场版"
	case "Series":
		return platform == "TV" || strings.EqualFold(platform, "web") || platform == "OVA"
	default:
		return false
	}
}

func (a *App) bangumiPoster(ctx context.Context, rawURL string) ([]byte, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed == nil || parsed.User != nil || parsed.Host != "lain.bgm.tv" {
		return nil, nil
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, nil
	}
	parsed.Scheme = "https"
	settings := a.tmdbSettings()
	return a.scraperDownloadArtwork(ctx, parsed.String(), settings.Directory)
}
