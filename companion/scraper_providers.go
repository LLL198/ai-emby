package main

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"strings"
)

type Scraper interface {
	Fetch(context.Context, *App, Item, string) ([]byte, error)
	Name() string
}

type orderedScrapers []string

type fanartScraper struct{}

type scraperXML struct {
	XMLName xml.Name
	Attr    []xml.Attr        `xml:",any,attr"`
	Fields  []scraperXMLField `xml:",any"`
}

type scraperXMLField struct {
	XMLName xml.Name
	Attr    []xml.Attr `xml:",any,attr"`
	Inner   string     `xml:",innerxml"`
}

func validScraperSelection(config scraperConfig) bool {
	selection := config.Scrapers
	if selection == nil {
		selection = []string{config.Scraper}
	}
	seen := make(map[string]struct{}, len(selection))
	for _, name := range selection {
		if _, duplicate := seen[name]; duplicate || lookupScraper(name) == nil {
			return false
		}
		seen[name] = struct{}{}
	}
	return len(selection) > 0
}

func (scrapers orderedScrapers) Name() string {
	return strings.Join([]string(scrapers), ", ")
}

func (scrapers orderedScrapers) Fetch(ctx context.Context, app *App, item Item, artwork string) ([]byte, error) {
	var mergedNFO []byte
	var failures []error

	for _, name := range scrapers {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		scraper := lookupScraper(name)
		if scraper == nil {
			failures = append(failures, fmt.Errorf("未知刮削器：%s", name))
			continue
		}
		data, err := scraper.Fetch(ctx, app, item, artwork)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if err == nil && len(data) == 0 {
			if item.Kind == "Episode" && artwork == "Still" {
				err = errTMDBNoArtwork
			} else {
				err = errors.New("刮削器返回空内容")
			}
		}
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if artwork != "NFO" {
			return data, nil
		}
		if len(mergedNFO) == 0 {
			var document scraperXML
			if err := xml.Unmarshal(data, &document); err != nil {
				failures = append(failures, err)
				continue
			}
			mergedNFO = data
			continue
		}
		merged, mergeErr := mergeScraperNFO(mergedNFO, data)
		if mergeErr != nil {
			failures = append(failures, mergeErr)
			continue
		}
		mergedNFO = merged
	}

	if len(mergedNFO) != 0 {
		return mergedNFO, nil
	}
	if len(failures) == 0 {
		return nil, errors.New("未选择刮削器")
	}
	var reportable []error
	for _, err := range failures {
		if errors.Is(err, errTMDBNoArtwork) || errors.Is(err, errTMDBEpisodeNotFound) {
			continue
		}
		reportable = append(reportable, err)
	}
	if len(reportable) == 0 {
		for _, err := range failures {
			if errors.Is(err, errTMDBEpisodeNotFound) {
				return nil, errTMDBEpisodeNotFound
			}
		}
		return nil, errors.Join(failures...)
	}
	return nil, joinUniqueScraperErrors(reportable)
}

func joinUniqueScraperErrors(failures []error) error {
	seen := make(map[string]struct{}, len(failures))
	unique := make([]error, 0, len(failures))
	for _, err := range failures {
		if err == nil {
			continue
		}
		message := err.Error()
		if _, exists := seen[message]; exists {
			continue
		}
		seen[message] = struct{}{}
		unique = append(unique, err)
	}
	return errors.Join(unique...)
}

func mergeScraperNFO(primary, secondary []byte) ([]byte, error) {
	var first, next scraperXML
	if err := xml.Unmarshal(primary, &first); err != nil {
		return nil, err
	}
	if err := xml.Unmarshal(secondary, &next); err != nil {
		return nil, err
	}
	if first.XMLName != next.XMLName {
		return nil, errors.New("NFO 类型不一致")
	}

	seen := make(map[string]struct{}, len(first.Fields))
	for _, field := range first.Fields {
		if strings.TrimSpace(field.Inner) != "" {
			seen[scraperNFOFieldKey(field)] = struct{}{}
		}
	}
	for _, field := range next.Fields {
		if strings.TrimSpace(field.Inner) == "" {
			continue
		}
		if _, exists := seen[scraperNFOFieldKey(field)]; !exists {
			key := scraperNFOFieldKey(field)
			replaced := false
			for i := range first.Fields {
				if scraperNFOFieldKey(first.Fields[i]) == key {
					first.Fields[i] = field
					replaced = true
					break
				}
			}
			if !replaced {
				first.Fields = append(first.Fields, field)
			}
		}
	}

	encoded, err := xml.MarshalIndent(first, "", "  ")
	if err != nil {
		return nil, err
	}
	return append([]byte(xml.Header), encoded...), nil
}

func scraperNFOFieldKey(field scraperXMLField) string {
	key := field.XMLName.Local
	for _, attr := range field.Attr {
		if attr.Name.Local == "type" {
			key += "|" + attr.Value
		}
	}
	return key
}

func (fanartScraper) Name() string { return "Fanart.tv" }

func (fanartScraper) Fetch(ctx context.Context, app *App, item Item, artwork string) ([]byte, error) {
	if artwork == "NFO" || artwork == "Still" || item.Kind == "Episode" {
		return nil, nil
	}
	return (tmdbScraper{fanart: true}).Fetch(ctx, app, item, artwork)
}

func init() {
	RegisterScraper(bangumiScraper{})
	RegisterScraper(fanartScraper{})
}
