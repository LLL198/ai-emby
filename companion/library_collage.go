package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

var libraryCollages = struct {
	sync.Mutex
	entries map[string]collageEntry
	bytes   int
}{entries: make(map[string]collageEntry)}

var collageSlots = make(chan struct{}, 2)

func collageVisibilityRevision(reader *mediaReader) string {
	rows, err := reader.Query("SELECT id FROM libraries ORDER BY id")
	if err != nil {
		return id()
	}
	defer rows.Close()
	var visible strings.Builder
	for rows.Next() {
		var libraryID string
		if rows.Scan(&libraryID) != nil {
			return id()
		}
		visible.WriteString(strconv.Itoa(len(libraryID)))
		visible.WriteByte(':')
		visible.WriteString(libraryID)
	}
	if rows.Err() != nil {
		return id()
	}
	return digest(visible.String())
}

func (a *App) collageArtworkPath(source string) string {
	source = strings.Replace(source, "/original/", "/w500/", 1)
	return filepath.Join(a.tmdbSettings().Directory, "images", digest(source)+".img")
}

func (a *App) collageSourceExists(source string) bool {
	if source == "cover" || strings.HasPrefix(source, "cover:") {
		return true
	}
	if strings.HasPrefix(source, "https://image.tmdb.org/") {
		info, err := os.Stat(a.collageArtworkPath(source))
		return err == nil && info.Mode().IsRegular() && info.Size() > 0 && info.Size() <= 5<<20
	}
	return safeImage(source) != ""
}

func (a *App) collageSource(reader *mediaReader, source, itemID string) []byte {
	if source == "cover" || strings.HasPrefix(source, "cover:") {
		if itemID == "favorites" {
			if reader.viewer.ID == "" || reader.viewer.API {
				return nil
			}
			if file, ok := userImageFile("images-sc", reader.viewer.ID); ok {
				return readCollageFile(file)
			}
			var data []byte
			if a.db.QueryRow("SELECT data FROM covers WHERE id=?", "favorite-cover:"+digest(reader.viewer.ID)).Scan(&data) == nil && len(data) <= 5<<20 {
				return data
			}
			return nil
		}
		if source != "cover" {
			itemID, _, _ = strings.Cut(strings.TrimPrefix(source, "cover:"), "|")
		}
		var data []byte
		if reader.QueryRow("SELECT data FROM covers WHERE id=?", itemID).Scan(&data) == nil && len(data) <= 5<<20 {
			return data
		}
		return nil
	}
	if strings.HasPrefix(source, "https://image.tmdb.org/") {
		data, _ := readArtworkCache(a.collageArtworkPath(source))
		return data
	}
	if file := safeImage(source); file != "" {
		return readCollageFile(file)
	}
	return nil
}

func readCollageFile(path string) []byte {
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > 5<<20 {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(file, (5<<20)+1))
	if err != nil || len(data) > 5<<20 {
		return nil
	}
	return data
}

func (a *App) collageItems(reader *mediaReader, libraryID string) []Item {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return nil
	}
	pivot := hex.EncodeToString(random[:])
	where := "lib=? AND kind IN ('Movie','Series')"
	var argument any = libraryID
	if libraryID == "favorites" {
		if reader.viewer.ID == "" || reader.viewer.API || !a.favoritesEnabled() {
			return nil
		}
		where = "kind IN ('Movie','Series') AND id IN (SELECT item FROM userdata_extra WHERE user_id=? AND favorite=1)"
		argument = reader.viewer.ID
	}
	items := make([]Item, 0, 48)
	seen := make(map[string]bool)
	load := func(statement string, args ...any) bool {
		rows, err := reader.Query(statement, args...)
		if err != nil {
			return false
		}
		defer rows.Close()
		for rows.Next() {
			item, err := readItem(rows)
			if err != nil {
				return false
			}
			if !seen[item.ID] {
				seen[item.ID] = true
				items = append(items, item)
			}
		}
		return rows.Err() == nil
	}
	for _, withArtwork := range []bool{true, false} {
		filter := where
		if withArtwork {
			filter += " AND (poster<>'' OR id IN (SELECT id FROM covers))"
		}
		for _, comparison := range []string{">=", "<"} {
			if !load("SELECT "+cols+" FROM items WHERE "+filter+" AND random_key "+comparison+" ? ORDER BY random_key LIMIT 24", argument, pivot) {
				return nil
			}
			if len(items) >= 24 {
				return items
			}
		}
	}
	if !load("SELECT "+cols+" FROM items WHERE "+where+" ORDER BY (poster<>'') DESC,name,id LIMIT 32", argument) {
		return nil
	}
	return items
}

func (a *App) renderLibraryCollage(reader *mediaReader, libraryID string) []byte {
	canvas := image.NewRGBA(image.Rect(0, 0, 640, 360))
	background := color.RGBA{20, 26, 38, 255}
	draw.Draw(canvas, canvas.Bounds(), image.NewUniform(background), image.Point{}, draw.Src)
	placed := 0
	for _, item := range a.collageItems(reader, libraryID) {
		if reader.ctx.Err() != nil {
			return nil
		}
		source := a.imagePathWithReader(reader, item.ID, "Primary")
		data := a.collageSource(reader, source, item.ID)
		config, _, err := image.DecodeConfig(bytes.NewReader(data))
		if err != nil || config.Width <= 0 || config.Height <= 0 || int64(config.Width)*int64(config.Height) > 12_000_000 {
			continue
		}
		picture, _, err := image.Decode(bytes.NewReader(data))
		if err != nil {
			continue
		}
		bounds := picture.Bounds()
		width, height := bounds.Dx(), bounds.Dy()
		cropWidth, cropHeight := width, height
		if width*276 > height*160 {
			cropWidth = max(1, height*160/276)
		} else {
			cropHeight = max(1, width*276/160)
		}
		xOffset, yOffset := (width-cropWidth)/2, (height-cropHeight)/2
		for y := 0; y < 276; y++ {
			for x := 0; x < 160; x++ {
				canvas.Set(placed*160+x, y, picture.At(bounds.Min.X+xOffset+x*cropWidth/160, bounds.Min.Y+yOffset+y*cropHeight/276))
			}
		}
		placed++
		if placed == 4 {
			break
		}
	}
	if placed == 0 {
		return nil
	}
	for y := 276; y < 360; y++ {
		alpha := float64(360-y) / 84 * 0.22
		for x := 0; x < 640; x++ {
			pixel := canvas.RGBAAt(x, 551-y)
			canvas.SetRGBA(x, y, color.RGBA{
				uint8(float64(pixel.R)*alpha + 20*(1-alpha)),
				uint8(float64(pixel.G)*alpha + 26*(1-alpha)),
				uint8(float64(pixel.B)*alpha + 38*(1-alpha)), 255,
			})
		}
	}
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, canvas, &jpeg.Options{Quality: 82}); err != nil {
		return nil
	}
	return encoded.Bytes()
}

func (a *App) serveLibraryCollage(w http.ResponseWriter, r *http.Request, libraryID string) {
	reader := a.mediaReader(r)
	revision := strconv.FormatInt(time.Now().Unix()/600, 10)
	if libraryID == "favorites" {
		revision += "|" + a.favoriteCollageRevision(reader)
	}
	key := digest(a.serverID + "|" + libraryID + "|" + reader.viewer.ID + "|" + reader.scope + "|" + revision)
	cached := func() []byte {
		libraryCollages.Lock()
		defer libraryCollages.Unlock()
		entry := libraryCollages.entries[key]
		if entry.until.IsZero() || time.Now().Before(entry.until) {
			return entry.data
		}
		return nil
	}
	data := cached()
	if len(data) == 0 {
		select {
		case collageSlots <- struct{}{}:
		case <-r.Context().Done():
			return
		}
		func() {
			defer func() { <-collageSlots }()
			data = cached()
			if len(data) == 0 {
				data = a.renderLibraryCollage(reader, libraryID)
				if len(data) != 0 {
					cacheLibraryCollage(key, data, time.Now().Add(10*time.Minute))
				}
			}
		}()
	}
	if len(data) == 0 {
		if r.Context().Err() == nil {
			fail(w, 404, "图片不存在")
		}
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	if !serveThumbnail(w, r, bytes.NewReader(data)) {
		http.ServeContent(w, r, "library-collage", time.Time{}, bytes.NewReader(data))
	}
}

func cacheLibraryCollage(key string, data []byte, until time.Time) {
	if len(data) == 0 || len(data) > 8<<20 {
		return
	}
	libraryCollages.Lock()
	defer libraryCollages.Unlock()
	if previous, ok := libraryCollages.entries[key]; ok {
		libraryCollages.bytes -= len(previous.data)
		delete(libraryCollages.entries, key)
	}
	now := time.Now()
	for cachedKey, entry := range libraryCollages.entries {
		if !entry.until.IsZero() && !now.Before(entry.until) {
			delete(libraryCollages.entries, cachedKey)
			libraryCollages.bytes -= len(entry.data)
		}
	}
	for len(libraryCollages.entries) >= 64 || libraryCollages.bytes+len(data) > 8<<20 {
		for cachedKey, entry := range libraryCollages.entries {
			delete(libraryCollages.entries, cachedKey)
			libraryCollages.bytes -= len(entry.data)
			break
		}
	}
	libraryCollages.entries[key] = collageEntry{data: data, until: until}
	libraryCollages.bytes += len(data)
}

func (a *App) libraryPoster(libraryID string) (string, Item) {
	return a.libraryPosterWithReader(a.mediaForUser(context.Background(), User{API: true}), libraryID)
}

func (a *App) libraryPosterWithReader(reader *mediaReader, libraryID string) (string, Item) {
	var root string
	if reader.QueryRow("SELECT path FROM libraries WHERE id=?", libraryID).Scan(&root) != nil {
		return "", Item{}
	}
	for _, name := range []string{"poster", "folder"} {
		for _, extension := range []string{".jpg", ".png", ".webp", ".jpeg"} {
			if source := safeImage(filepath.Join(root, name+extension)); source != "" {
				return source, Item{}
			}
		}
	}
	rows, err := reader.Query("SELECT "+cols+" FROM items WHERE lib=? AND kind IN ('Movie','Series') ORDER BY (poster<>'') DESC,name,id LIMIT 32", libraryID)
	if err != nil {
		return "", Item{}
	}
	items := []Item{}
	for rows.Next() {
		item, err := readItem(rows)
		if err != nil {
			rows.Close()
			return "", Item{}
		}
		items = append(items, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return "", Item{}
	}
	var fallback Item
	for _, item := range items {
		if source := a.imagePathWithReader(reader, item.ID, "Primary"); source != "" && a.collageSourceExists(source) {
			return "collage:" + libraryID, Item{}
		}
		if fallback.ID == "" && a.tmdbImageEligible(item, "Primary") {
			fallback = item
		}
	}
	return "", fallback
}
