package main

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type catalogCoversKey struct{}

type listStateDeferredKey struct{}

func listStateDeferred(r *http.Request) bool {
	return r != nil && r.Context().Value(listStateDeferredKey{}) == true
}

func deferListState(r *http.Request) *http.Request {
	if r == nil {
		return nil
	}
	return r.WithContext(context.WithValue(r.Context(), listStateDeferredKey{}, true))
}

func (a *App) prepareCardList(r *http.Request, user User, items []Item) (*http.Request, error) {
	if r == nil {
		return nil, nil
	}
	r = deferListState(r)
	covers := make(map[string]bool)
	reader := a.mediaForUser(r.Context(), user)
	for start := 0; start < len(items); start += 200 {
		args := make([]any, 0, min(200, len(items)-start))
		for _, item := range items[start:min(start+200, len(items))] {
			args = append(args, item.ID)
		}
		rows, err := reader.Query("SELECT id FROM covers WHERE id IN ("+catalogPlaceholders(len(args))+")", args...)
		if err != nil {
			return r, err
		}
		for rows.Next() {
			var itemID string
			if err = rows.Scan(&itemID); err != nil {
				rows.Close()
				return r, err
			}
			covers[itemID] = true
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return r, err
		}
	}
	r = r.WithContext(context.WithValue(r.Context(), catalogCoversKey{}, covers))
	return a.prepareCatalogBatch(r, user, items)
}

func (a *App) applyCardCover(item Item, r *http.Request, dto M) {
	if r == nil || a.db == nil {
		return
	}
	var present bool
	covers, batched := r.Context().Value(catalogCoversKey{}).(map[string]bool)
	if batched {
		present = covers[item.ID]
	} else {
		var exists int
		present = a.mediaReader(r).QueryRow("SELECT 1 FROM covers WHERE id=?", item.ID).Scan(&exists) == nil
	}
	if present {
		tags, ok := dto["ImageTags"].(M)
		if !ok {
			tags = M{}
			dto["ImageTags"] = tags
		}
		tags["Primary"] = a.imageTag(item.ID, "Primary", "cover")
	}
}

// catalogLike escapes SQL LIKE metacharacters for literal catalog searches.
func catalogLike(value string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(value)
}

func (a *App) applyCardFields(item Item, r *http.Request, user User, dto M) {
	metadata := a.metadataForList(item, r)
	remote := a.cachedTMDBForList(item, r)
	a.applyDisplayName(item, dto, metadata, remote)
	dto["CanDelete"] = false
	dto["CanDownload"] = false
	if metadata.Plot != "" {
		dto["Overview"] = metadata.Plot
	} else if remote.Overview != "" && item.Overview == "" {
		dto["Overview"] = remote.Overview
	}
	if metadata.Rating > 0 {
		dto["CommunityRating"] = metadata.Rating
	} else if remote.Rating > 0 {
		dto["CommunityRating"] = remote.Rating
	}
	dto["Genres"] = append([]string{}, metadata.Genres...)
	if len(metadata.Genres) == 0 {
		genres := []string{}
		for _, genre := range remote.Genres {
			if genre.Name != "" {
				genres = append(genres, genre.Name)
			}
		}
		dto["Genres"] = genres
	}
	dto["OfficialRating"] = metadata.MPAA
	dto["OriginalTitle"] = metadata.OriginalTitle
	if date := premiereDate(metadata, item.Year); date != "" {
		dto["PremiereDate"] = date + "T00:00:00.0000000Z"
	}
	ids := M{}
	if metadata.TMDB != "" {
		ids["Tmdb"] = metadata.TMDB
	}
	if metadata.IMDB != "" {
		ids["Imdb"] = metadata.IMDB
	}
	for _, id := range metadata.UniqueIDs {
		if strings.EqualFold(id.Type, "tmdb") {
			ids["Tmdb"] = id.Value
		}
		if strings.EqualFold(id.Type, "imdb") {
			ids["Imdb"] = id.Value
		}
	}
	if len(ids) == 0 {
		_, _, fallback := localDisplayFallback(item)
		ids = fallback
	}
	dto["ProviderIds"] = ids
	dto["People"] = []M{}
	if requestedField(r, "People") {
		people := []M{}
		for _, person := range metadata.Actors {
			if person.Name != "" {
				people = append(people, M{"Id": personID(person.Name), "Name": person.Name, "Role": person.Role, "Type": "Actor"})
			}
		}
		for _, name := range metadata.Directors {
			if name != "" {
				people = append(people, M{"Id": personID(name), "Name": name, "Type": "Director"})
			}
		}
		dto["People"] = people
	}
	studios := []M{}
	for _, name := range metadata.Studios {
		if name = strings.TrimSpace(name); name != "" {
			id, _ := strconv.ParseInt(digest("studio:" + name)[:13], 16, 64)
			studios = append(studios, M{"Id": id, "Name": name})
		}
	}
	dto["Studios"] = studios
	dto["Taglines"] = []string{}
	if metadata.Tagline != "" {
		dto["Taglines"] = []string{metadata.Tagline}
	}
	dto["Chapters"] = a.introChapters(item)
	dto["MediaAttachments"] = []M{}
	dto["ExternalUrls"] = []M{}
	dto["RemoteTrailers"] = []M{}
	dto["LockedFields"] = []string{}
	dto["LockData"] = false
	a.enrichMediaForList(item, r, dto)
	if item.Kind == "Series" || item.Kind == "Season" {
		dto["ChildCount"] = a.childCountForList(item, r)
	}
	if batch := catalogBatchFrom(r); batch != nil && (item.Kind == "Season" || item.Kind == "Episode") {
		parent := batch.items[item.Parent]
		if item.Kind == "Episode" && parent.Kind == "Series" {
			season := flatSeasonItem(parent, item.Season)
			dto["SeasonId"], dto["SeasonName"] = season.ID, season.Name
		}
		if parent.Kind == "Season" {
			dto["SeasonId"] = parent.ID
			dto["SeasonName"] = parent.Name
			parent = batch.items[parent.Parent]
		}
		if parent.Kind == "Series" {
			dto["SeriesId"] = parent.ID
			dto["SeriesName"] = parent.Name
		}
	}
}

func catalogCreatedAt(item Item) string {
	created := item.AddedAt
	if created == 0 {
		created = item.Mtime
	}
	return time.Unix(0, created).UTC().Format(time.RFC3339)
}
