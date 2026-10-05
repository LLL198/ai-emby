package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type featureMetadata struct {
	sidecar
	Year      int      `xml:"year"`
	Season    int      `xml:"season"`
	Episode   int      `xml:"episode"`
	Countries []string `xml:"country"`
	Tags      []string `xml:"tag"`
	Locked    bool     `xml:"-"`
	CachedAt  int64    `xml:"-"`
}

func (a *App) featureMetadataView(x Item) featureMetadata {
	m := featureMetadata{sidecar: a.metadata(x), Year: x.Year, Season: x.Season, Episode: x.Episode, Countries: []string{}, Tags: []string{}}
	var raw string
	var locked int
	if a.db.QueryRow("SELECT data,locked FROM feature_metadata WHERE item=?", x.ID).Scan(&raw, &locked) == nil {
		_ = json.Unmarshal([]byte(raw), &m)
		m.Locked = locked == 1
	}
	if m.Title == "" {
		m.Title = x.Name
	}
	if m.Plot == "" {
		m.Plot = x.Overview
	}
	return m
}

func (a *App) featureStoreMetadata(ctx context.Context, x Item, m featureMetadata, locked bool, force ...bool) error {
	m.Locked = locked
	if m.CachedAt == 0 {
		m.CachedAt = featureNow()
	}
	ids := m.UniqueIDs[:0]
	for _, entry := range m.UniqueIDs {
		if !strings.EqualFold(entry.Type, "tmdb") && !strings.EqualFold(entry.Type, "imdb") {
			ids = append(ids, entry)
		}
	}
	m.UniqueIDs = ids
	for _, entry := range []struct {
		Type  string `xml:"type,attr"`
		Value string `xml:",chardata"`
	}{{Type: "tmdb", Value: m.TMDB}, {Type: "imdb", Value: m.IMDB}} {
		if entry.Value != "" {
			m.UniqueIDs = append(m.UniqueIDs, entry)
		}
	}
	tx, err := a.db.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "SET LOCAL ai_emby.metadata_write='true'"); err != nil {
		return err
	}
	forced := len(force) > 0 && force[0]
	result, err := tx.ExecContext(ctx, "INSERT INTO feature_metadata(item,data,locked,updated) VALUES($1,$2,$3,$4) ON CONFLICT(item) DO UPDATE SET data=excluded.data,locked=excluded.locked,updated=excluded.updated WHERE feature_metadata.locked=0 OR excluded.locked=1 OR $5=1", x.ID, featureJSON(m), boolInt(locked), featureNow(), boolInt(forced))
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return nil
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO item_metadata(item,data) VALUES($1,$2) ON CONFLICT(item) DO UPDATE SET data=excluded.data", x.ID, featureJSON(m.sidecar)); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE items SET name=$1,sort_name=$1,overview=$2,year=$3,season=$4,episode=$5,premiere_date=$6 WHERE id=$7", m.Title, m.Plot, m.Year, m.Season, m.Episode, m.Premiered, x.ID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM item_people WHERE item=$1", x.ID); err != nil {
		return err
	}
	for _, p := range m.Actors {
		if p.Name == "" {
			continue
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO item_people(item,person,name,role,type,thumb) VALUES($1,$2,$3,$4,'Actor',$5) ON CONFLICT DO NOTHING", x.ID, personID(p.Name), p.Name, p.Role, p.Thumb); err != nil {
			return err
		}
	}
	for _, p := range m.Directors {
		if _, err = tx.ExecContext(ctx, "INSERT INTO item_people(item,person,name,role,type,thumb) VALUES($1,$2,$3,'','Director','') ON CONFLICT DO NOTHING", x.ID, personID(p), p); err != nil {
			return err
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	return a.featureSaveFileCache(x, m)
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
func featureDataRoot() string {
	root := os.Getenv("MEDIA_INFO_ROOT")
	if root == "" {
		root = "/app/data"
	}
	return filepath.Join(root, "features")
}
func featureCacheFile(x Item) string {
	return filepath.Join(featureDataRoot(), "metadata", digest(x.Path)+".json")
}
func (a *App) featureSaveFileCache(x Item, m featureMetadata) error {
	path := featureCacheFile(x)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".metadata-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(data)
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), path)
}

func (a *App) featureMetadataAPI(w http.ResponseWriter, r *http.Request) {
	if !featureMethod(w, r, http.MethodGet, http.MethodPut, http.MethodDelete) {
		return
	}
	if r.Method == http.MethodDelete {
		x, err := a.item(r.URL.Query().Get("ID"))
		if err != nil {
			featureError(w, err)
			return
		}
		if _, ok := a.reserveConcurrentScan(x.Lib); !ok {
			fail(w, 409, "媒体库正在扫描，完成后再恢复本地资料")
			return
		}
		if _, err = a.db.Exec("DELETE FROM feature_metadata WHERE item=?", x.ID); err != nil {
			a.finishConcurrentScan(x.Lib)
			featureError(w, err)
			return
		}
		_ = os.Remove(featureCacheFile(x))
		go a.runConcurrentScan(x.Lib, false, false, nil)
		respond(w, M{"ok": true})
		return
	}
	if r.Method == http.MethodGet {
		x, err := a.item(r.URL.Query().Get("ID"))
		if err != nil {
			featureError(w, err)
			return
		}
		respond(w, a.featureMetadataView(x))
		return
	}
	var request struct {
		ID       string
		Metadata featureMetadata
		WriteNFO bool
	}
	if !body(w, r, &request) {
		return
	}
	x, err := a.item(request.ID)
	if err != nil {
		featureError(w, err)
		return
	}
	m := request.Metadata
	m.CachedAt = featureNow()
	if strings.TrimSpace(m.Title) == "" || len(m.Title) > 1024 || len(m.Plot) > 100000 || m.Year < 0 || m.Year > 9999 || m.Season < 0 || m.Episode < 0 || len(m.Tags) > 100 || len(m.Countries) > 100 || m.Rating < 0 || m.Rating > 10 {
		fail(w, 400, "请检查标题、年份、评分与季集编号")
		return
	}
	if m.TMDB != "" {
		n, e := strconv.Atoi(m.TMDB)
		if e != nil || n < 1 {
			fail(w, 400, "TMDB ID 无效")
			return
		}
	}
	if err = a.featureStoreMetadata(r.Context(), x, m, m.Locked, true); err != nil {
		featureError(w, err)
		return
	}
	if request.WriteNFO {
		if err = a.featureWriteNFO(x, m); err != nil {
			fail(w, 502, "资料已保存，NFO 写入失败："+a.scraperSafeError(err).Error())
			return
		}
	}
	respond(w, m)
}

func featureNFOPath(x Item) string {
	switch x.Kind {
	case "Series":
		return filepath.Join(x.Path, "tvshow.nfo")
	case "Season":
		return filepath.Join(x.Path, "season.nfo")
	}
	return strings.TrimSuffix(x.Path, filepath.Ext(x.Path)) + ".nfo"
}

func (a *App) featureWriteNFO(x Item, m featureMetadata) error {
	root, rootErr := a.scraperRoot(x.Lib, featureNFOPath(x))
	if rootErr != nil {
		return rootErr
	}
	kind := "movie"
	if x.Kind == "Series" {
		kind = "tvshow"
	} else if x.Kind == "Season" {
		kind = "season"
	} else if x.Kind == "Episode" {
		kind = "episodedetails"
	}
	wrapper := struct {
		XMLName xml.Name
		featureMetadata
	}{xml.Name{Local: kind}, m}
	data, err := xml.MarshalIndent(wrapper, "", "  ")
	if err != nil {
		return err
	}
	// Retain unknown elements from existing NFOs while explicitly replacing editable fields.
	var primary, existing scraperXML
	if xml.Unmarshal(data, &primary) == nil {
		if previous, e := safeSidecarBytes(featureNFOPath(x)); e == nil && xml.Unmarshal(previous, &existing) == nil && primary.XMLName.Local == existing.XMLName.Local {
			replaced := map[string]bool{}
			for _, name := range []string{"title", "originaltitle", "plot", "year", "rating", "premiered", "season", "episode", "tmdbid", "imdbid", "uniqueid", "genre", "country", "tag", "mpaa", "tagline"} {
				replaced[name] = true
			}
			for _, field := range primary.Fields {
				replaced[field.XMLName.Local] = true
			}
			for _, field := range existing.Fields {
				if !replaced[field.XMLName.Local] {
					primary.Fields = append(primary.Fields, field)
				}
			}
			data, err = xml.MarshalIndent(primary, "", "  ")
			if err != nil {
				return err
			}
		}
	}
	return a.scraperWrite(root, scraperTarget{Path: featureNFOPath(x), Content: "NFO"}, x, append([]byte(xml.Header), data...), true)
}
func safeSidecarBytes(path string) ([]byte, error) {
	real, err := filepath.EvalSymlinks(path)
	if err != nil || !allowedMediaPath(real) {
		return nil, sql.ErrNoRows
	}
	f, err := os.Open(real)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, 2<<20))
}

func (a *App) featureIdentifyAPI(w http.ResponseWriter, r *http.Request) {
	if !featureMethod(w, r, http.MethodGet, http.MethodPost) {
		return
	}
	if r.Method == http.MethodGet {
		mediaType := r.URL.Query().Get("Type")
		if mediaType != "movie" && mediaType != "tv" {
			fail(w, 400, "请选择电影或剧集")
			return
		}
		query := strings.TrimSpace(r.URL.Query().Get("Query"))
		if query == "" || len(query) > 512 {
			fail(w, 400, "请输入作品名或 TMDB ID")
			return
		}
		settings := a.tmdbSettings()
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		items := []M{}
		if n, err := strconv.Atoi(query); err == nil && n > 0 {
			var data map[string]any
			if err = a.tmdbGet(ctx, "/"+mediaType+"/"+query, url.Values{"language": {"zh-CN"}}, &data, settings); err != nil {
				featureError(w, a.scraperSafeError(err))
				return
			}
			items = append(items, featureCandidate(data, mediaType))
		} else {
			var data struct{ Results []map[string]any }
			if err := a.tmdbGet(ctx, "/search/"+mediaType, url.Values{"query": {query}, "language": {"zh-CN"}}, &data, settings); err != nil {
				featureError(w, a.scraperSafeError(err))
				return
			}
			for _, candidate := range data.Results {
				items = append(items, featureCandidate(candidate, mediaType))
			}
		}
		respond(w, M{"Items": items})
		return
	}
	var request struct {
		ID, TMDB, Type string
		WriteNFO       bool
	}
	if !body(w, r, &request) {
		return
	}
	x, err := a.item(request.ID)
	if err != nil {
		featureError(w, err)
		return
	}
	if x.Kind != "Movie" && x.Kind != "Series" {
		fail(w, 400, "单集和季使用所属剧集的识别结果，请重新识别剧集")
		return
	}
	expected := "movie"
	if x.Kind == "Series" {
		expected = "tv"
	}
	n, e := strconv.Atoi(request.TMDB)
	if request.Type != expected || e != nil || n < 1 {
		fail(w, 400, "作品类型或 TMDB ID 无效")
		return
	}
	x.scraperTMDBID = request.TMDB
	m, err := a.featureFetchMetadata(r.Context(), x, request.Type, request.TMDB)
	if err != nil {
		featureError(w, a.scraperSafeError(err))
		return
	}
	if err = a.featureStoreMetadata(r.Context(), x, m, true); err != nil {
		featureError(w, err)
		return
	}
	if request.WriteNFO {
		if err = a.featureWriteNFO(x, m); err != nil {
			fail(w, 502, "识别已保存，NFO 写入失败")
			return
		}
	}
	// Correct series IDs flow into episode requests through tmdbIdentity.
	if x.Kind == "Series" {
		_, _ = a.featureStartLibraryJob(x.Lib, "metadata", token(r), r.UserAgent())
	}
	respond(w, m)
}
func featureCandidate(data map[string]any, kind string) M {
	name, date := data["title"], data["release_date"]
	if kind == "tv" {
		name, date = data["name"], data["first_air_date"]
	}
	poster, _ := data["poster_path"].(string)
	return M{"ID": data["id"], "Type": kind, "Name": name, "Date": date, "Overview": data["overview"], "Poster": tmdbArtwork(poster)}
}

func (a *App) featureFetchMetadata(ctx context.Context, x Item, kind, provider string) (featureMetadata, error) {
	settings := a.tmdbSettings()
	endpoint := ""
	if provider != "" {
		endpoint = "/" + kind + "/" + provider
	} else {
		_, _, resolved, e := a.resolveTMDBItem(ctx, x, settings)
		if e != nil {
			return featureMetadata{}, e
		}
		endpoint = resolved
	}
	var data tmdbData
	if err := a.tmdbGet(ctx, endpoint, url.Values{"language": {"zh-CN"}, "append_to_response": {"credits"}}, &data, settings); err != nil {
		return featureMetadata{}, err
	}
	local := a.metadata(x)
	nfo, err := tmdbNFO(x, data, local)
	if err != nil {
		return featureMetadata{}, err
	}
	m := featureMetadata{Year: x.Year, Season: x.Season, Episode: x.Episode}
	if err = xml.Unmarshal(nfo, &m.sidecar); err != nil {
		return m, err
	}
	if m.Title == "" {
		m.Title = x.Name
	}
	date := tmdbNFODate(x.Kind, data)
	if len(date) >= 4 {
		m.Year, _ = strconv.Atoi(date[:4])
	}
	for _, actor := range data.Credits.Cast {
		var p struct {
			Name  string `xml:"name"`
			Role  string `xml:"role"`
			Thumb string `xml:"thumb"`
		}
		p.Name = actor.Name
		p.Role = actor.Character
		p.Thumb = tmdbArtwork(actor.Profile)
		m.Actors = append(m.Actors, p)
		if len(m.Actors) >= 40 {
			break
		}
	}
	for _, p := range data.Credits.Crew {
		if p.Job == "Director" {
			m.Directors = append(m.Directors, p.Name)
		}
	}
	if len(m.Actors) == 0 {
		m.Actors = local.Actors
	}
	if len(m.Directors) == 0 {
		m.Directors = local.Directors
	}
	m.Streams = local.Streams
	m.Runtime = data.Runtime
	if m.Runtime == 0 && len(data.EpisodeRuntime) > 0 {
		m.Runtime = data.EpisodeRuntime[0]
	}
	if m.Runtime == 0 {
		m.Runtime = local.Runtime
	}
	var extras struct {
		Countries           []string `json:"origin_country"`
		ProductionCountries []struct {
			Name string `json:"name"`
		} `json:"production_countries"`
	}
	// The same cached response includes the supplementary classification fields.
	raw, _ := json.Marshal(data)
	_ = json.Unmarshal(raw, &extras)
	m.Countries = extras.Countries
	for _, c := range extras.ProductionCountries {
		m.Countries = append(m.Countries, c.Name)
	}
	key, _ := a.tmdbIdentity(x)
	_ = writeTMDB(settings.Directory, tmdbRecord{Key: key, Until: 0, Data: data})
	return m, nil
}

func (a *App) featureMetadataForJob(ctx context.Context, x Item, policy featureLibraryPolicy) error {
	var raw string
	var locked int
	if a.db.QueryRow("SELECT data,locked FROM feature_metadata WHERE item=?", x.ID).Scan(&raw, &locked) == nil && locked == 1 {
		return a.featureFillArtwork(ctx, x, policy)
	}
	var m featureMetadata
	if bytes, err := os.ReadFile(featureCacheFile(x)); err == nil && json.Unmarshal(bytes, &m) == nil && m.Title != "" && (m.Locked || featureNow()-m.CachedAt < 30*86400) {
		if err = a.featureStoreMetadata(ctx, x, m, m.Locked); err != nil {
			return err
		}
		return a.featureFillArtwork(ctx, x, policy)
	}
	m, err := a.featureFetchMetadata(ctx, x, "", "")
	if err != nil {
		return err
	}
	if err = a.featureStoreMetadata(ctx, x, m, false); err != nil {
		return err
	}
	if policy.WriteNFO {
		if a.featureMetadataView(x).Locked {
			return a.featureFillArtwork(ctx, x, policy)
		}
		if err = a.featureWriteNFO(x, m); err != nil {
			return err
		}
	}
	return a.featureFillArtwork(ctx, x, policy)
}

func featureMissingError(err error) bool {
	return errors.Is(err, errTMDBNoArtwork) || errors.Is(err, os.ErrNotExist)
}

func (a *App) featureProcessLibrary(ctx context.Context, lib, action, t, ua, job string) error {
	rows, err := a.db.Query("SELECT "+cols+" FROM items WHERE lib=? ORDER BY CASE kind WHEN 'Series' THEN 0 WHEN 'Season' THEN 1 ELSE 2 END,id", lib)
	if err != nil {
		return err
	}
	items := []Item{}
	for rows.Next() {
		x, e := readItem(rows)
		if e != nil {
			rows.Close()
			return e
		}
		items = append(items, x)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	policy := a.featurePolicy(lib)
	failures, done := 0, 0
	var lastErr error
	a.changeActivity(job, func(e *activityEntry) { e.State = "running"; e.Total = len(items) })
	concurrency := scraperConcurrency(a.scraperSettings().Concurrency)
	if action == "probe" {
		concurrency = a.probeSettings().Concurrency
	}
	if action == "capture" || action == "intro" {
		concurrency = min(concurrency, 4)
	}
	var lock sync.Mutex
	// Resolve parent groups first, then process files concurrently within each group.
	for _, kindGroup := range [][]string{{"Series"}, {"Season"}, {"Movie", "Episode"}} {
		jobs := make(chan Item)
		var workers sync.WaitGroup
		for n := 0; n < concurrency; n++ {
			workers.Add(1)
			go func() {
				defer workers.Done()
				for x := range jobs {
					if ctx.Err() != nil {
						continue
					}
					var e error
					switch action {
					case "metadata":
						e = a.featureMetadataForJob(ctx, x, policy)
					case "images":
						if x.Kind == "Episode" && x.AddedAt >= time.Now().Add(-30*24*time.Hour).UnixNano() && !a.featureHasArtwork(x, "Primary") {
							e = a.featureFillArtwork(ctx, x, policy)
						}
					case "probe":
						if x.URL != "" {
							_, e = a.extractMedia(ctx, x, t, ua)
						}
					case "capture":
						if x.URL != "" && !a.featureHasArtwork(x, "Primary") {
							e = a.featureCapture(ctx, x, 0)
						}
					case "intro":
						if x.Kind == "Episode" {
							e = a.featureExtractChapters(ctx, x)
						}
					}
					lock.Lock()
					done++
					if e != nil && !featureMissingError(e) {
						failures++
						lastErr = e
					}
					a.changeActivity(job, func(entry *activityEntry) {
						entry.Done = done
						entry.Current = fmt.Sprintf("并发 %d · 完成 %d/%d · 失败 %d · %s", concurrency, done, len(items), failures, x.Name)
					})
					lock.Unlock()
					if e != nil && !featureMissingError(e) {
						child := a.newActivity("metadata-item", x.ID, x.Name)
						a.changeActivity(child, func(entry *activityEntry) { entry.TaskID = job })
						a.finishActivity(child, a.scraperSafeError(e))
					}
				}
			}()
		}
		for _, x := range items {
			matches := false
			for _, kind := range kindGroup {
				if x.Kind == kind {
					matches = true
				}
			}
			if !matches {
				continue
			}
			select {
			case jobs <- x:
			case <-ctx.Done():
			}
		}
		close(jobs)
		workers.Wait()
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	if failures > 0 {
		return fmt.Errorf("%d 项失败：%w", failures, a.scraperSafeError(lastErr))
	}
	return nil
}
