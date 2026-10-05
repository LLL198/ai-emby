package main

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/lib/pq"
)

// Batch writes preserve display names, Chinese titles, timestamps and existing metadata.
const concurrentScanUpsertSuffix = ` ON CONFLICT(path) DO UPDATE SET
parent=excluded.parent,
name=COALESCE((SELECT name FROM media_display_names WHERE item=excluded.id),CASE WHEN excluded.name=CASE WHEN excluded.kind IN ('Movie','Episode') THEN regexp_replace(regexp_replace(excluded.path,'^.*/',''),'\.[^.]*$','') ELSE regexp_replace(excluded.path,'^.*/','') END OR (items.name ~ '[一-鿿]' AND excluded.name !~ '[一-鿿]') THEN COALESCE(NULLIF(items.name,''),excluded.name) ELSE excluded.name END),
kind=excluded.kind,url=excluded.url,
overview=COALESCE(NULLIF(excluded.overview,''),items.overview),
poster=COALESCE(NULLIF(excluded.poster,''),items.poster),
year=CASE WHEN excluded.year>0 THEN excluded.year ELSE items.year END,
season=excluded.season,episode=excluded.episode,mtime=excluded.mtime,size=excluded.size,seen=excluded.seen,
added_at=CASE WHEN items.kind IN ('Movie','Episode') AND excluded.kind IN ('Movie','Episode') AND (items.url,items.mtime,items.size) IS DISTINCT FROM (excluded.url,excluded.mtime,excluded.size) THEN (extract(epoch from clock_timestamp())*1000000000)::bigint ELSE items.added_at END`

func scanValues(rows, columns int) string {
	row := "(" + strings.TrimSuffix(strings.Repeat("?,", columns), ",") + ")"
	return strings.TrimSuffix(strings.Repeat(row+",", rows), ",")
}

func scanPremiereDate(meta sidecar) string {
	date := strings.TrimSpace(meta.Premiered)
	if len(date) >= 10 {
		date = date[:10]
	}
	if _, err := time.Parse("2006-01-02", date); err == nil {
		return date
	}
	return ""
}

func (cache *scanCache) actorImage(item Item, name, thumb string) string {
	directory := filepath.Dir(item.Path)
	if item.Kind == "Series" || item.Kind == "Season" {
		directory = item.Path
	}
	for _, path := range []string{filepath.Join(directory, ".actors", strings.ReplaceAll(name, " ", "_")+".jpg"), filepath.Join(directory, thumb)} {
		if cache.isRegular(path) {
			if safe := cache.safeImage(path); safe != "" {
				return safe
			}
		}
	}
	// Reuse the original URL validation without repeating known-absent local
	// actor paths for every episode of the same series.
	if strings.HasPrefix(thumb, "https://image.tmdb.org/t/p/") {
		return thumb
	}
	return ""
}

func (a *App) writeScanBatch(ctx context.Context, cache *scanCache, batch []parsedScanItem, generation string, newItems *[]string) error {
	if len(batch) == 0 {
		return nil
	}
	tx, err := a.db.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var added []string
	if newItems != nil {
		paths := make([]string, 0, len(batch))
		for _, parsed := range batch {
			if parsed.Item.Kind == "Movie" || parsed.Item.Kind == "Episode" {
				paths = append(paths, parsed.Item.Path)
			}
		}
		rows, err := tx.QueryContext(ctx, "SELECT path FROM items WHERE path=ANY($1)", pq.Array(paths))
		if err != nil {
			return err
		}
		existing := make(map[string]bool, len(paths))
		for rows.Next() {
			var path string
			if err = rows.Scan(&path); err != nil {
				break
			}
			existing[path] = true
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			return err
		}
		for _, parsed := range batch {
			if (parsed.Item.Kind == "Movie" || parsed.Item.Kind == "Episode") && !existing[parsed.Item.Path] {
				added = append(added, parsed.Item.ID)
				existing[parsed.Item.Path] = true
			}
		}
	}
	if _, err = tx.ExecContext(ctx, "SELECT set_config('go_emby.scanner_upsert','true',true)"); err != nil {
		return err
	}
	itemIDs := make([]string, 0, len(batch))
	for _, parsed := range batch {
		itemIDs = append(itemIDs, parsed.Item.ID)
	}
	metadataRows, err := tx.QueryContext(ctx, "SELECT item,data FROM item_metadata WHERE item=ANY($1)", pq.Array(itemIDs))
	if err != nil {
		return err
	}
	previousMetadata := make(map[string]sidecar, len(batch))
	for metadataRows.Next() {
		var itemID, raw string
		if err = metadataRows.Scan(&itemID, &raw); err != nil {
			metadataRows.Close()
			return err
		}
		var metadata sidecar
		if json.Unmarshal([]byte(raw), &metadata) == nil {
			previousMetadata[itemID] = metadata
		}
	}
	err = metadataRows.Err()
	metadataRows.Close()
	if err != nil {
		return err
	}
	args := make([]any, 0, len(batch)*15)
	metadataArgs := make([]any, 0, len(batch)*2)
	dateArgs := make([]any, 0, len(batch)*2)
	byID := make(map[string]parsedScanItem, len(batch))
	for _, parsed := range batch {
		item := parsed.Item
		parsed.Metadata = mergeRefreshSidecar(item, parsed.Metadata, previousMetadata[item.ID])
		args = append(args, item.ID, item.Lib, item.Parent, item.Name, item.Kind, item.Path, item.URL, item.Overview, item.Poster, item.Year, item.Season, item.Episode, item.Mtime, item.Size, generation)
		data, err := json.Marshal(parsed.Metadata)
		if err != nil {
			return err
		}
		metadataArgs = append(metadataArgs, item.ID, string(data))
		dateArgs = append(dateArgs, item.ID, scanPremiereDate(parsed.Metadata))
		byID[item.ID] = parsed
	}
	sql := "INSERT INTO items(id,lib,parent,name,kind,path,url,overview,poster,year,season,episode,mtime,size,seen) VALUES " + scanValues(len(batch), 15) + concurrentScanUpsertSuffix
	if _, err = tx.ExecContext(ctx, bind(sql), args...); err != nil {
		return fmt.Errorf("批量保存媒体索引失败：%w", err)
	}
	metadataSQL := "INSERT INTO item_metadata(item,data) VALUES " + scanValues(len(batch), 2) + " ON CONFLICT(item) DO UPDATE SET data=excluded.data WHERE item_metadata.data IS DISTINCT FROM excluded.data RETURNING item"
	rows, err := tx.QueryContext(ctx, bind(metadataSQL), metadataArgs...)
	if err != nil {
		return err
	}
	changed := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		changed = append(changed, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if len(changed) > 0 {
		if _, err = tx.ExecContext(ctx, "DELETE FROM item_people WHERE item=ANY($1)", pq.Array(changed)); err != nil {
			return err
		}
		people := map[string][]any{}
		for _, id := range changed {
			parsed := byID[id]
			for _, actor := range parsed.Metadata.Actors {
				if actor.Name == "" {
					continue
				}
				person := personID(actor.Name)
				people[id+"\x00"+person+"\x00Actor"] = []any{id, person, actor.Name, actor.Role, "Actor", cache.actorImage(parsed.Item, actor.Name, actor.Thumb)}
			}
			for _, name := range parsed.Metadata.Directors {
				if name != "" {
					person := personID(name)
					people[id+"\x00"+person+"\x00Director"] = []any{id, person, name, "", "Director", ""}
				}
			}
		}
		// Chunk person inserts so a large NFO cannot exceed PostgreSQL's bind
		// parameter limit or require one round trip per actor.
		peopleArgs := []any{}
		flushPeople := func() error {
			if len(peopleArgs) == 0 {
				return nil
			}
			_, err := tx.ExecContext(ctx, bind("INSERT INTO item_people(item,person,name,role,type,thumb) VALUES "+scanValues(len(peopleArgs)/6, 6)+" ON CONFLICT(item,person,type) DO UPDATE SET name=excluded.name,role=excluded.role,thumb=excluded.thumb"), peopleArgs...)
			peopleArgs = peopleArgs[:0]
			return err
		}
		for _, person := range people {
			peopleArgs = append(peopleArgs, person...)
			if len(peopleArgs) >= 1200 {
				if err = flushPeople(); err != nil {
					return err
				}
			}
		}
		if err = flushPeople(); err != nil {
			return err
		}
	}
	dateSQL := "UPDATE items i SET sort_name=i.name,premiere_date=CASE WHEN source.premiere<>'' THEN source.premiere ELSE i.premiere_date END FROM (VALUES " + scanValues(len(batch), 2) + ") AS source(id,premiere) WHERE i.id=source.id"
	if _, err = tx.ExecContext(ctx, bind(dateSQL), dateArgs...); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	if newItems != nil {
		*newItems = append(*newItems, added...)
	}
	return nil
}
