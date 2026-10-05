package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

type pageToken struct {
	Query    string
	Values   []string
	Position int
	Expires  int64
	Count    int
	Pivot    string
	Wrapped  bool
}
type pageEntry struct {
	Token string
	Until time.Time
}
type pageCache struct {
	sync.Mutex
	Entries map[string]pageEntry
}

func (a *App) pageSignature(r *http.Request, where, order string, args []any) string {
	b, _ := json.Marshal([]any{where, order, args, token(r), q(r, "Limit"), q(r, "EnableTotalRecordCount"), q(r, "RandomSeed")})
	return digest(string(b))
}
func (a *App) encodePage(p pageToken) string {
	b, _ := json.Marshal(p)
	h := hmac.New(sha256.New, []byte(a.cursorSecret))
	h.Write(b)
	return base64.RawURLEncoding.EncodeToString(b) + "." + base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}
func (a *App) decodePage(s, key string) (pageToken, error) {
	var p pageToken
	if len(s) > 8192 {
		return p, errors.New("invalid cursor")
	}
	parts := strings.Split(s, ".")
	if len(parts) != 2 {
		return p, errors.New("invalid cursor")
	}
	b, e := base64.RawURLEncoding.DecodeString(parts[0])
	if e != nil {
		return p, e
	}
	sig, e := base64.RawURLEncoding.DecodeString(parts[1])
	if e != nil {
		return p, e
	}
	h := hmac.New(sha256.New, []byte(a.cursorSecret))
	h.Write(b)
	if !hmac.Equal(sig, h.Sum(nil)) {
		return p, errors.New("invalid cursor signature")
	}
	if e = json.Unmarshal(b, &p); e != nil {
		return p, e
	}
	if p.Query != key || p.Expires < time.Now().Unix() {
		return p, errors.New("cursor expired or query changed; restart from first page")
	}
	if p.Position < 0 || p.Count < -1 {
		return p, errors.New("invalid cursor position")
	}
	return p, nil
}
func (a *App) pageStart(r *http.Request, key string) (pageToken, error) {
	start, e := strconv.Atoi(q(r, "StartIndex"))
	if q(r, "StartIndex") == "" {
		start = 0
		e = nil
	}
	if e != nil || start < 0 {
		return pageToken{}, errors.New("invalid StartIndex")
	}
	cursor := q(r, "Cursor")
	if cursor == "" && start > 0 {
		a.pages.Lock()
		v := a.pages.Entries[key+":"+strconv.Itoa(start)]
		a.pages.Unlock()
		if time.Now().Before(v.Until) {
			cursor = v.Token
		}
		if cursor == "" {
			return pageToken{Query: key, Position: start, Count: -1, Expires: time.Now().Add(15 * time.Minute).Unix()}, nil
		}
	}
	if cursor != "" {
		p, e := a.decodePage(cursor, key)
		if e == nil && start != 0 && start != p.Position {
			return p, errors.New("cursor position mismatch")
		}
		return p, e
	}
	return pageToken{Query: key, Count: -1, Expires: time.Now().Add(15 * time.Minute).Unix()}, nil
}
func (a *App) savePage(p pageToken) string {
	s := a.encodePage(p)
	a.pages.Lock()
	defer a.pages.Unlock()
	if a.pages.Entries == nil {
		a.pages.Entries = map[string]pageEntry{}
	}
	if len(a.pages.Entries) >= 2048 {
		now := time.Now()
		for k, v := range a.pages.Entries {
			if now.After(v.Until) {
				delete(a.pages.Entries, k)
			}
		}
		if len(a.pages.Entries) >= 2048 {
			for k := range a.pages.Entries {
				delete(a.pages.Entries, k)
				break
			}
		}
	}
	a.pages.Entries[p.Query+":"+strconv.Itoa(p.Position)] = pageEntry{s, time.Unix(p.Expires, 0)}
	return s
}
func itemSortValue(x Item, col string) string {
	switch col {
	case "id":
		return x.ID
	case "name":
		return x.Name
	case "year":
		return strconv.Itoa(x.Year)
	case "mtime":
		return strconv.FormatInt(x.Mtime, 10)
	case "added_at":
		return strconv.FormatInt(x.AddedAt, 10)
	case "premiere_date":
		return x.PremiereDate
	case "sort_name":
		return x.SortName
	case "random_key":
		return x.RandomKey
	case "season":
		return strconv.Itoa(x.Season)
	case "episode":
		return strconv.Itoa(x.Episode)
	}
	return ""
}
func (a *App) pageItems(r *http.Request, u User, where string, args []any, order string, limit int) ([]Item, pageToken, string, bool, error) {
	if limit < 1 || limit > 10000 {
		return nil, pageToken{}, "", false, errors.New("invalid page limit")
	}
	if strings.TrimSpace(q(r, "SearchTerm")) != "" {
		return a.relevancePage(r, u, where, args, order, limit)
	}
	if strings.HasPrefix(order, "random_key ") {
		return a.randomPage(r, where, args, order, limit)
	}
	if order == "resume" {
		order = "resumeupdated DESC,id DESC"
		if strings.EqualFold(q(r, "IncludePlayedAtEnd"), "true") {
			order = "resumegroup DESC," + order
		}
	}
	return a.browsePlanPage(r, u, where, args, order, limit)
}

// Listing avoids sidecar parsing, actor lookups, filesystem image discovery and probing.
func (a *App) listDTO(x Item, r *http.Request, u User) M {
	// Clients such as Infuse use episode lists as their detail payload.
	if hasBrowseDetailFields(r) && catalogBatchFrom(r) == nil {
		m := a.viewerDTO(x, r, u)
		if requestedField(r, "Etag") {
			b, _ := json.Marshal(m)
			m["Etag"] = digest(string(b))
		}
		return m
	}
	folder := x.Kind == "Series" || x.Kind == "Season"
	m := M{"Id": x.ID, "ServerId": a.serverID, "ParentId": x.Parent, "Name": x.Name, "SortName": x.Name, "Overview": x.Overview, "Type": x.Kind, "IsFolder": folder, "MediaType": "Video", "LocationType": "FileSystem", "ProductionYear": x.Year, "IndexNumber": x.Episode, "ParentIndexNumber": x.Season, "ImageTags": M{}, "BackdropImageTags": []string{}, "PrimaryImageAspectRatio": 2.0 / 3, "DateCreated": catalogCreatedAt(x), "UserData": M{"Played": false, "IsFavorite": false, "PlaybackPositionTicks": 0, "Key": x.ID}}
	if x.Poster != "" {
		m["ImageTags"] = M{"Primary": a.listImageTag(x)}
	}
	if x.Kind == "Episode" {
		m["PrimaryImageAspectRatio"] = 16.0 / 9
		if path := episodeThumb(x); path != "" {
			m["ImageTags"].(M)["Thumb"] = a.imageTag(x.ID, "Thumb", path)
		}
	}
	if u.API {
		m["Path"] = x.Path
		if x.URL != "" && strings.Contains(strings.ToLower(q(r, "Fields")), "mediasources") {
			m["MediaSources"] = []M{a.source(x, token(r))}
		}
	}
	if !u.API && r != nil && x.URL != "" && strings.Contains(strings.ToLower(q(r, "Fields")), "mediasources") {
		m["MediaSources"] = a.versionSourcesForList(x, r, u)
		m["MediaSourceCount"] = len(m["MediaSources"].([]M))
	}
	if hasBrowseDetailFields(r) {
		a.applyCardFields(x, r, u, m)
	}
	if x.Kind == "Movie" {
		delete(m, "IndexNumber")
		delete(m, "ParentIndexNumber")
	}

	if x.Kind == "Season" {
		m["IndexNumber"] = x.Season
		m["SeriesId"] = x.Parent
	}
	if requestedField(r, "Etag") {
		data, _ := json.Marshal(m)
		m["Etag"] = digest(string(data))
	}
	return m
}

func (a *App) listDTOs(items []Item, r *http.Request, u User) []M {
	if prepared, err := a.prepareCardList(r, u, items); err == nil {
		r = prepared
	}
	out := make([]M, 0, len(items))
	for _, x := range items {
		m := a.listDTO(x, r, u)
		a.applyCardCover(x, r, m)
		if !m["IsFolder"].(bool) && m["MediaSourceCount"] == nil {
			m["MediaSourceCount"] = 1
		}
		out = append(out, m)
		a.applyViewerImageTags(m, u)
	}
	_ = a.fillPlayState(r, u, out)
	a.fillEpisodeCounts(items, out, u)
	return out
}

func (a *App) listImageTag(x Item) string {
	h := hmac.New(sha256.New, []byte(a.cursorSecret))
	h.Write([]byte("list-primary|" + x.ID + "|" + x.Poster + "|" + strconv.FormatInt(x.Mtime, 10)))
	return fmt.Sprintf("%x", h.Sum(nil))[:32]
}

func requestedField(r *http.Request, name string) bool {
	if r == nil {
		return false
	}
	for _, field := range strings.Split(q(r, "Fields"), ",") {
		if strings.EqualFold(strings.TrimSpace(field), name) {
			return true
		}
	}
	return false
}
func hasBrowseDetailFields(r *http.Request) bool {
	return requestedField(r, "Overview") || requestedField(r, "Genres") || requestedField(r, "ProviderIds") || requestedField(r, "Etag") || requestedField(r, "AlternateMediaSources")
}
func browseMediaDetails(r *http.Request) bool {
	// A season list (even Limit=1) does not identify the episode being viewed.
	ids := strings.TrimSpace(q(r, "Ids"))
	return requestedField(r, "MediaSources") && requestedField(r, "Overview") && ids != "" && !strings.Contains(ids, ",")
}
