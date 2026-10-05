package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

type trackingMediaIdentity struct {
	Title, Kind, TMDBID string
	Year                int
}

var trackingEpisodeTitle = regexp.MustCompile(`(?i)(?:更新\s*(?:至|到)?\s*\d+\s*集|(?:全|共|第)\s*\d+\s*集|\d+\s*集|S\d{1,3}E\d{1,4}|第\s*\d+\s*季|Season\s*\d+)`)

func (a *App) trackingImportHint(resourceID, subscriptionID string) trackingMediaIdentity {
	var subscriptionRaw, resourceRaw string
	var err error
	if resourceID != "" {
		err = a.db.QueryRow("SELECT s.data,r.data FROM feature_tracking_resources r JOIN feature_tracking_subscriptions s ON s.id=r.subscription WHERE r.id=?", resourceID).Scan(&subscriptionRaw, &resourceRaw)
	} else if subscriptionID != "" {
		err = a.db.QueryRow(`SELECT s.data,coalesce(r.data,'{}') FROM feature_tracking_subscriptions s
LEFT JOIN feature_tracking_imports i ON i.subscription=s.id
LEFT JOIN feature_tracking_resources r ON r.id=coalesce(nullif(s.data::jsonb#>>'{AutoImport,ResourceID}',''),i.data::jsonb->>'ResourceID')
WHERE s.id=$1`, subscriptionID).Scan(&subscriptionRaw, &resourceRaw)
	} else {
		return trackingMediaIdentity{}
	}
	if err != nil {
		return trackingMediaIdentity{}
	}
	var subscription trackingSubscription
	var resource trackingResource
	if json.Unmarshal([]byte(subscriptionRaw), &subscription) != nil || json.Unmarshal([]byte(resourceRaw), &resource) != nil {
		return trackingMediaIdentity{}
	}
	return trackingSearchIdentity(subscription, resource)
}

func trackingSearchIdentity(s trackingSubscription, resource trackingResource) trackingMediaIdentity {
	identity := trackingMediaIdentity{Title: strings.TrimSpace(s.Title), Year: s.Year}
	if trackingTitleMatch(resource.Title, s) {
		if identity.Year == 0 {
			years := map[string]bool{}
			for _, span := range trackingYearPattern.FindAllStringIndex(resource.Title, -1) {
				asciiWord := func(b byte) bool { return b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' }
				if span[0] > 0 && asciiWord(resource.Title[span[0]-1]) || span[1] < len(resource.Title) && asciiWord(resource.Title[span[1]]) {
					continue
				}
				years[resource.Title[span[0]:span[1]]] = true
			}
			if len(years) == 1 {
				for year := range years {
					identity.Year, _ = strconv.Atoi(year)
				}
			}
		}
		if trackingEpisodeTitle.MatchString(resource.Title) {
			identity.Kind = "tv"
		}
	}
	return identity
}

func (a *App) trackingResolveIdentity(ctx context.Context, s trackingSubscription, resource trackingResource) (trackingMediaIdentity, error) {
	identity := trackingSearchIdentity(s, resource)
	if s.ItemID != "" {
		if item, err := a.item(s.ItemID); err == nil && (item.Kind == "Movie" || item.Kind == "Series") {
			identity.Kind = "movie"
			if item.Kind == "Series" {
				identity.Kind = "tv"
			}
			identity.TMDBID = a.metadata(item).TMDB
		}
	}
	resolver := namingTMDBResolver{app: a, settings: a.tmdbSettings(), cache: map[string]namingTMDBResult{}}
	request, _, err := resolver.resolve(ctx, namingSourceInfo{Kind: identity.Kind, Identity: MediaRecognition{Title: identity.Title, Year: identity.Year, TMDBID: identity.TMDBID}}, namingRequest{})
	if err != nil {
		return identity, err
	}
	identity.Title, identity.Year, identity.Kind, identity.TMDBID = request.Title, request.Year, request.Kind, request.TMDB
	return identity, nil
}

func (a *App) trackingValidateIdentityLibrary(library string, identity trackingMediaIdentity) error {
	for _, lib := range a.libraries() {
		if lib["Id"] != library {
			continue
		}
		if identity.Kind == "tv" && lib["CollectionType"] == "movies" {
			return errors.New("搜索结果识别为剧集，请选择剧集媒体库和对应 STRM 目录，避免按电影刮削")
		}
		if identity.Kind == "movie" && lib["CollectionType"] == "tvshows" {
			return errors.New("搜索结果识别为电影，请选择电影媒体库和对应 STRM 目录")
		}
	}
	return nil
}

func (a *App) trackingIdentityForItem(ctx context.Context, item Item) (*trackingMediaIdentity, error) {
	var stateRaw, subscriptionRaw, resourceRaw string
	err := a.db.QueryRowContext(ctx, `SELECT i.data,s.data,coalesce(r.data,'{}') FROM feature_tracking_imports i
JOIN feature_tracking_subscriptions s ON s.id=i.subscription
LEFT JOIN feature_tracking_resources r ON r.id=i.data::jsonb->>'ResourceID'
WHERE coalesce(i.data::jsonb->>'Output','')<>'' AND
($1=i.data::jsonb->>'Output' OR left($1,length(i.data::jsonb->>'Output')+1)=(i.data::jsonb->>'Output')||$2)
ORDER BY length(i.data::jsonb->>'Output') DESC LIMIT 1`, item.Path, string(filepath.Separator)).Scan(&stateRaw, &subscriptionRaw, &resourceRaw)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	var state trackingImportState
	var subscription trackingSubscription
	var resource trackingResource
	if json.Unmarshal([]byte(stateRaw), &state) != nil || json.Unmarshal([]byte(subscriptionRaw), &subscription) != nil || json.Unmarshal([]byte(resourceRaw), &resource) != nil {
		return nil, errors.New("追新作品识别记录无法读取，请重新入库")
	}
	identity := state.Identity
	if identity == nil {
		fallback := trackingSearchIdentity(subscription, resource)
		identity = &fallback
	}
	if strings.TrimSpace(identity.Title) == "" {
		return nil, nil
	}
	if err := a.trackingValidateIdentityLibrary(item.Lib, *identity); err != nil {
		return nil, err
	}
	if identity.Kind == "tv" && item.Kind == "Movie" || identity.Kind == "movie" && item.Kind != "Movie" {
		return nil, errors.New("追新作品类型与媒体索引不一致，请按正确的电影或剧集类型重新入库")
	}
	return identity, nil
}
