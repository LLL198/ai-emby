package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
)

const catalogBatchSize = 200

type catalogBatchKey struct{}

type catalogBatch struct {
	metadata     map[string]sidecar
	media        map[string]M
	child        map[string]int
	versions     map[string][]Item
	remote       map[string]tmdbData
	items        map[string]Item
	tmdb         tmdbConfig
	nanShareFast bool
}

func catalogPlaceholders(count int) string {
	if count < 1 {
		return ""
	}
	return strings.TrimRight(strings.Repeat("?,", count), ",")
}

func catalogBatchFrom(r *http.Request) *catalogBatch {
	if r == nil {
		return nil
	}
	batch, _ := r.Context().Value(catalogBatchKey{}).(*catalogBatch)
	return batch
}

func (a *App) prepareCatalogBatch(r *http.Request, user User, items []Item) (*http.Request, error) {
	batch := &catalogBatch{metadata: map[string]sidecar{}, media: map[string]M{}, child: map[string]int{}, versions: map[string][]Item{}, remote: map[string]tmdbData{}, items: map[string]Item{}, tmdb: a.tmdbSettings(), nanShareFast: a.nanShareFastEnabled()}
	for _, item := range items {
		batch.items[item.ID] = item
	}
	reader := a.mediaForUser(r.Context(), user)
	if requestedField(r, "MediaSources") || requestedField(r, "AlternateMediaSources") {
		group := a.versionGroupingFor("candidate")
		for start := 0; start < len(items); start += catalogBatchSize {
			args := []any{}
			for _, item := range items[start:min(start+catalogBatchSize, len(items))] {
				if item.URL != "" {
					args = append(args, item.ID)
				}
			}
			if len(args) == 0 {
				continue
			}
			rows, err := reader.Query("SELECT selected.id,"+prefixedCols("candidate")+" FROM items selected JOIN items candidate ON "+group+"="+a.versionGroupingFor("selected")+" WHERE selected.id IN ("+catalogPlaceholders(len(args))+") AND candidate.url<>'' ORDER BY selected.id,candidate.id", args...)
			if err != nil {
				return r, err
			}
			for rows.Next() {
				var selected string
				var item Item
				err = rows.Scan(&selected, &item.ID, &item.Lib, &item.Parent, &item.Name, &item.Kind, &item.Path, &item.URL, &item.Overview, &item.Poster, &item.Year, &item.Season, &item.Episode, &item.Mtime, &item.Size, &item.AddedAt, &item.PremiereDate, &item.SortName, &item.RandomKey)
				if err != nil {
					rows.Close()
					return r, err
				}
				batch.versions[selected] = append(batch.versions[selected], item)
				batch.items[item.ID] = item
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return r, err
			}
		}
	}
	for depth := 0; depth < 2; depth++ {
		parents := []any{}
		seen := map[string]bool{}
		for _, item := range batch.items {
			if item.Parent != "" && item.Parent != item.Lib {
				if _, ok := batch.items[item.Parent]; !ok && !seen[item.Parent] {
					parents = append(parents, item.Parent)
					seen[item.Parent] = true
				}
			}
		}
		for start := 0; start < len(parents); start += catalogBatchSize {
			ids := parents[start:min(start+catalogBatchSize, len(parents))]
			rows, err := reader.Query("SELECT "+cols+" FROM items WHERE id IN ("+catalogPlaceholders(len(ids))+")", ids...)
			if err != nil {
				return r, err
			}
			for rows.Next() {
				item, err := readItem(rows)
				if err != nil {
					rows.Close()
					return r, err
				}
				batch.items[item.ID] = item
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return r, err
			}
		}
	}
	ids := make([]any, 0, len(batch.items))
	for id := range batch.items {
		ids = append(ids, id)
	}
	for start := 0; start < len(ids); start += catalogBatchSize {
		args := ids[start:min(start+catalogBatchSize, len(ids))]
		rows, err := reader.Query("SELECT item,data FROM item_metadata WHERE item IN ("+catalogPlaceholders(len(args))+")", args...)
		if err != nil {
			return r, err
		}
		for rows.Next() {
			var id, raw string
			if err = rows.Scan(&id, &raw); err != nil {
				break
			}
			var metadata sidecar
			if json.Unmarshal([]byte(raw), &metadata) == nil {
				batch.metadata[id] = metadata
			}
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			return r, err
		}
		rows, err = reader.Query("SELECT parent,count(*) FROM items WHERE parent IN ("+catalogPlaceholders(len(args))+") GROUP BY parent", args...)
		if err != nil {
			return r, err
		}
		for rows.Next() {
			var parent string
			var count int
			if err = rows.Scan(&parent, &count); err != nil {
				break
			}
			batch.child[parent] = count
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			return r, err
		}
	}
	if err := a.loadCatalogMediaBatch(reader, batch, ids); err != nil {
		return r, err
	}
	return r.WithContext(context.WithValue(r.Context(), catalogBatchKey{}, batch)), nil
}

func (a *App) loadCatalogMediaBatch(reader *mediaReader, batch *catalogBatch, ids []any) error {
	for start := 0; start < len(ids); start += catalogBatchSize {
		args := ids[start:min(start+catalogBatchSize, len(ids))]
		rows, err := reader.Query("SELECT item,source,data FROM media_probe WHERE item IN ("+catalogPlaceholders(len(args))+")", args...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id, source, raw string
			if err = rows.Scan(&id, &source, &raw); err != nil {
				break
			}
			item, exists := batch.items[id]
			if !exists || digest(item.URL) != source {
				continue
			}
			var media M
			if json.Unmarshal([]byte(raw), &media) == nil {
				batch.media[id] = media
			}
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			return err
		}
	}
	sources := map[string][]string{}
	for id, item := range batch.items {
		if item.URL != "" && batch.media[id] == nil {
			source := digest(item.URL)
			sources[source] = append(sources[source], id)
		}
	}
	keys := []any{}
	for key := range sources {
		keys = append(keys, key)
	}
	for start := 0; start < len(keys); start += catalogBatchSize {
		args := keys[start:min(start+catalogBatchSize, len(keys))]
		rows, err := reader.Query("SELECT DISTINCT ON (source) source,data FROM media_probe WHERE source IN ("+catalogPlaceholders(len(args))+") ORDER BY source,item", args...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var source, raw string
			if err = rows.Scan(&source, &raw); err != nil {
				break
			}
			var media M
			if json.Unmarshal([]byte(raw), &media) == nil && media["Partial"] != true {
				for _, id := range sources[source] {
					batch.media[id] = media
				}
			}
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func (a *App) cachedMediaForList(item Item, r *http.Request) M {
	if batch := catalogBatchFrom(r); batch != nil {
		return batch.media[item.ID]
	}
	return a.cachedMedia(item)
}

func (a *App) childCountForList(item Item, r *http.Request) int {
	if batch := catalogBatchFrom(r); batch != nil {
		return batch.child[item.ID]
	}
	var count int
	_ = a.mediaReader(r).QueryRow("SELECT count(*) FROM items WHERE parent=?", item.ID).Scan(&count)
	return count
}

func (a *App) metadataForList(item Item, r *http.Request) sidecar {
	if batch := catalogBatchFrom(r); batch != nil {
		return batch.metadata[item.ID]
	}
	return a.metadata(item)
}

func (a *App) cachedTMDBForList(item Item, r *http.Request) tmdbData {
	batch := catalogBatchFrom(r)
	if batch == nil {
		return a.cachedTMDB(item)
	}
	if !batch.tmdb.Enabled {
		return tmdbData{}
	}
	if cached, ok := batch.remote[item.ID]; ok {
		return cached
	}
	key, _ := tmdbIdentityWith(item, func(id string) (Item, error) {
		if item, ok := batch.items[id]; ok {
			return item, nil
		}
		return Item{}, sql.ErrNoRows
	}, func(item Item) sidecar { return batch.metadata[item.ID] })
	var data tmdbData
	if record, ok := readTMDB(key, batch.tmdb.Directory); ok && record.Until == 0 {
		data = record.Data
	} else if item.Kind == "Movie" || item.Kind == "Series" {
		if record, ok := readTMDB(key+"|primary", batch.tmdb.Directory); ok && record.Until == 0 {
			data = record.Data
		}
	}
	batch.remote[item.ID] = data
	return data
}

func (a *App) enrichMediaForList(item Item, r *http.Request, dto M) {
	enrichSidecarMedia(a.metadataForList(item, r), dto)
	mergeCachedMedia(dto, a.cachedMediaForList(item, r))
}

func (a *App) playURLForList(item Item, r *http.Request) string {
	container := "mkv"
	name := strings.ToLower(item.Name + " " + filepath.Base(item.Path))
	path := strings.Split(strings.ToLower(item.URL), "?")[0]
	for _, candidate := range []string{"mp4", "mkv", "avi", "ts", "m2ts", "mov", "webm"} {
		if strings.Contains(name, "."+candidate) || strings.HasSuffix(path, "."+candidate) {
			container = candidate
			break
		}
	}
	if cached, ok := a.cachedMediaForList(item, r)["Container"].(string); ok {
		for _, candidate := range []string{"mp4", "mkv", "avi", "ts", "m2ts", "mov", "webm"} {
			if cached == candidate {
				container = cached
				break
			}
		}
	}
	return "/emby/Videos/" + item.ID + "/stream." + container + "?Static=true&api_key=" + url.QueryEscape(token(r))
}

func (a *App) viewerSourceForList(item Item, r *http.Request, user User) M {
	media := a.source(item, token(r))
	a.enrichMediaForList(item, r, media)
	playbackURL := a.playURLForList(item, r)
	fast := false
	if batch := catalogBatchFrom(r); batch != nil {
		fast = batch.nanShareFast
	} else {
		fast = a.nanShareFastEnabled()
	}
	if !user.API && fast {
		playbackURL = fastPlaybackSourceURL(playbackURL, item.URL)
	}
	viewerSourceURL(media, playbackURL, r, user)
	return media
}

func (a *App) versionSourcesForList(item Item, r *http.Request, user User) []M {
	batch := catalogBatchFrom(r)
	if batch == nil {
		return a.versionSources(item, r, user, true)
	}
	versions := batch.versions[item.ID]
	if len(versions) == 0 || user.API {
		versions = []Item{item}
	}
	out := make([]M, 0, len(versions))
	for _, version := range versions {
		media := a.viewerSourceForList(version, r, user)
		media["Name"] = filepath.Base(version.Path)
		media["ItemId"] = version.ID
		media["LibraryId"] = version.Lib
		out = append(out, media)
	}
	return out
}
