package main

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/draw"
	_ "image/jpeg"
	"image/png"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func featureArtworkKind(kind string) bool {
	return kind == "Primary" || kind == "Backdrop" || kind == "Logo"
}
func featureArtworkContent(x Item, kind string) string {
	if kind == "Primary" {
		if x.Kind == "Episode" {
			return "Still"
		}
		return "Poster"
	}
	return kind
}
func (a *App) featureHasArtwork(x Item, kind string) bool {
	var n int
	_ = a.db.QueryRow("SELECT count(*) FROM feature_artwork WHERE item=? AND kind=?", x.ID, kind).Scan(&n)
	return n > 0 || a.imagePath(x.ID, kind) != ""
}

func (a *App) featureSaveArtwork(ctx context.Context, x Item, kind string, data []byte, writeDisk bool) error {
	if !featureArtworkKind(kind) {
		return errors.New("图片类型无效")
	}
	if err := scraperValidateImage(data); err != nil {
		return err
	}
	if _, err := a.db.DB.ExecContext(ctx, "INSERT INTO feature_artwork(item,kind,mime,data,updated) VALUES($1,$2,$3,$4,$5) ON CONFLICT(item,kind) DO UPDATE SET mime=excluded.mime,data=excluded.data,updated=excluded.updated", x.ID, kind, http.DetectContentType(data), data, featureNow()); err != nil {
		return err
	}
	cache := featureArtworkFile(x, kind)
	if err := os.MkdirAll(filepath.Dir(cache), 0700); err == nil {
		f, e := os.CreateTemp(filepath.Dir(cache), ".image-*")
		if e == nil {
			_, e = f.Write(data)
			closeErr := f.Close()
			if e == nil && closeErr == nil {
				_ = os.Rename(f.Name(), cache)
			}
			_ = os.Remove(f.Name())
		}
	}
	if writeDisk {
		content := featureArtworkContent(x, kind)
		target := scraperFilename(content, x.Kind, x.Path)
		root, err := a.scraperRoot(x.Lib, target)
		if err != nil {
			return err
		}
		return a.scraperWrite(root, scraperTarget{Path: target, Content: content}, x, data, true)
	}
	return nil
}

func (a *App) featureArtworkAPI(w http.ResponseWriter, r *http.Request) {
	if !featureMethod(w, r, http.MethodGet, http.MethodPost, http.MethodDelete) {
		return
	}
	x, err := a.item(r.URL.Query().Get("ID"))
	if err != nil {
		featureError(w, err)
		return
	}
	kind := r.URL.Query().Get("Kind")
	if kind == "" {
		kind = "Primary"
	}
	if !featureArtworkKind(kind) {
		fail(w, 400, "图片类型无效")
		return
	}
	if r.Method == http.MethodDelete {
		if _, err = a.db.Exec("DELETE FROM feature_artwork WHERE item=? AND kind=?", x.ID, kind); err != nil {
			featureError(w, err)
			return
		}
		_ = os.Remove(featureArtworkFile(x, kind))
		respond(w, M{"ok": true})
		return
	}
	if r.Method == http.MethodGet {
		result := M{"ID": x.ID, "Kind": kind, "Manual": false, "Candidates": []M{}}
		var count int
		_ = a.db.QueryRow("SELECT count(*) FROM feature_artwork WHERE item=? AND kind=?", x.ID, kind).Scan(&count)
		result["Manual"] = count > 0
		if r.URL.Query().Get("Online") == "true" {
			_, _, endpoint, e := a.resolveTMDBItem(r.Context(), x, a.tmdbSettings())
			if e != nil {
				featureError(w, a.scraperSafeError(e))
				return
			}
			var data map[string][]struct {
				Path     string `json:"file_path"`
				Language string `json:"iso_639_1"`
				Width    int
				Height   int
			}
			if e = a.tmdbGet(r.Context(), endpoint+"/images", nil, &data, a.tmdbSettings()); e != nil {
				featureError(w, a.scraperSafeError(e))
				return
			}
			key := "posters"
			if x.Kind == "Episode" {
				key = "stills"
			}
			if kind == "Backdrop" {
				key = "backdrops"
			}
			if kind == "Logo" {
				key = "logos"
			}
			choices := []M{}
			for _, v := range data[key] {
				choices = append(choices, M{"URL": tmdbArtwork(v.Path), "Language": v.Language, "Width": v.Width, "Height": v.Height})
				if len(choices) >= 60 {
					break
				}
			}
			result["Candidates"] = choices
		}
		respond(w, result)
		return
	}
	var data []byte
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		var request struct {
			URL     string
			Capture bool
			Seconds float64
		}
		if !body(w, r, &request) {
			return
		}
		if request.Capture {
			if err = a.featureCapture(r.Context(), x, request.Seconds); err != nil {
				featureError(w, a.scraperSafeError(err))
				return
			}
			respond(w, M{"ok": true})
			return
		}
		parsed, e := url.Parse(request.URL)
		if e != nil || parsed.Scheme != "https" || parsed.Host != "image.tmdb.org" || !strings.HasPrefix(parsed.Path, "/t/p/") {
			fail(w, 400, "请选择 TMDB 图片或上传本地图片")
			return
		}
		data, err = a.scraperDownloadArtwork(r.Context(), parsed.String(), a.tmdbSettings().Directory)
	} else {
		data, err = io.ReadAll(http.MaxBytesReader(w, r.Body, maxArtworkBytes))
	}
	if err != nil {
		fail(w, 400, "读取图片失败")
		return
	}
	if err = a.featureSaveArtwork(r.Context(), x, kind, data, r.URL.Query().Get("WriteDisk") == "true"); err != nil {
		featureError(w, a.scraperSafeError(err))
		return
	}
	respond(w, M{"ok": true})
}

func (a *App) featureArtworkImage(w http.ResponseWriter, r *http.Request, user User) {
	if !featureMethod(w, r, http.MethodGet, http.MethodHead) {
		return
	}
	item := r.URL.Query().Get("ID")
	var x Item
	var err error
	if strings.HasPrefix(item, "library:") {
		lib := strings.TrimPrefix(item, "library:")
		if !a.featureLibraryAllowed(user, lib) {
			fail(w, 404, "图片不存在")
			return
		}
		x.ID = item
	} else {
		x, err = a.featureAccessibleItem(user, item)
		if err != nil {
			featureError(w, err)
			return
		}
	}
	kind := r.URL.Query().Get("Kind")
	if kind == "" {
		kind = "Primary"
	}
	var data []byte
	var mime string
	var updated int64
	if err = a.db.QueryRow("SELECT data,mime,updated FROM feature_artwork WHERE item=? AND kind=?", x.ID, kind).Scan(&data, &mime, &updated); err != nil {
		fail(w, 404, "没有自选图片")
		return
	}
	w.Header().Set("Content-Type", mime)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, "image", time.Unix(updated, 0), bytes.NewReader(data))
}

func (a *App) featureFillArtwork(ctx context.Context, x Item, policy featureLibraryPolicy) error {
	for _, kind := range []string{"Primary", "Backdrop"} {
		if a.featureHasArtwork(x, kind) {
			continue
		}
		if x.Kind == "Episode" && kind != "Primary" {
			continue
		}
		if data, err := os.ReadFile(featureArtworkFile(x, kind)); err == nil && scraperValidateImage(data) == nil {
			if err = a.featureSaveArtwork(ctx, x, kind, data, policy.WriteArtwork); err != nil {
				return err
			}
			continue
		}
		data, err := (tmdbScraper{}).Fetch(ctx, a, x, featureArtworkContent(x, kind))
		if featureMissingError(err) {
			continue
		}
		if err != nil {
			return err
		}
		if err = a.featureSaveArtwork(ctx, x, kind, data, policy.WriteArtwork); err != nil {
			return err
		}
	}
	return nil
}

type featureCoverStyle struct {
	Font string
	Size int
	Wrap bool
	Blur int
}

func featureArtworkFile(x Item, kind string) string {
	return filepath.Join(featureDataRoot(), "artwork", digest(x.Path)+"-"+kind+".img")
}
func (a *App) featureCoverAPI(w http.ResponseWriter, r *http.Request) {
	if !featureMethod(w, r, http.MethodGet, http.MethodPut, http.MethodPost) {
		return
	}
	style := featureCoverStyle{Font: "system-ui", Size: 20, Wrap: true, Blur: 0}
	a.featureSetting("cover-style", &style)
	if r.Method == http.MethodGet {
		respond(w, style)
		return
	}
	if r.Method == http.MethodPut {
		if !body(w, r, &style) {
			return
		}
		if style.Size < 12 || style.Size > 48 || style.Blur < 0 || style.Blur > 30 || len(style.Font) > 80 {
			fail(w, 400, "封面文字大小 12–48，模糊范围 0–30")
			return
		}
		if err := a.saveFeatureSetting("cover-style", style); err != nil {
			featureError(w, err)
			return
		}
		respond(w, style)
		return
	}
	var request struct{ Library string }
	if !body(w, r, &request) {
		return
	}
	rows, err := a.db.Query("SELECT "+cols+" FROM items WHERE lib=? AND kind IN ('Movie','Series') ORDER BY added_at DESC LIMIT 24", request.Library)
	if err != nil {
		featureError(w, err)
		return
	}
	items := []Item{}
	for rows.Next() {
		x, e := readItem(rows)
		if e == nil {
			items = append(items, x)
		}
	}
	rows.Close()
	canvas := image.NewRGBA(image.Rect(0, 0, 900, 600))
	draw.Draw(canvas, canvas.Bounds(), &image.Uniform{C: color.RGBA{225, 237, 247, 255}}, image.Point{}, draw.Src)
	pictures := []image.Image{}
	for _, x := range items {
		var data []byte
		if a.db.QueryRow("SELECT data FROM feature_artwork WHERE item=? AND kind='Primary'", x.ID).Scan(&data) != nil {
			if path := a.imagePath(x.ID, "Primary"); path != "" {
				data, _ = safeSidecarBytes(path)
			}
		}
		pic, _, e := image.Decode(bytes.NewReader(data))
		if e == nil {
			pictures = append(pictures, pic)
		}
		if len(pictures) >= 6 {
			break
		}
	}
	if len(pictures) == 0 {
		fail(w, 409, "媒体库暂时没有可用海报，请先补全图片")
		return
	}
	for i := 0; i < 6; i++ {
		pic := pictures[i%len(pictures)]
		bounds := pic.Bounds()
		for y := 0; y < 300; y++ {
			for xx := 0; xx < 300; xx++ {
				sx := bounds.Min.X + xx*bounds.Dx()/300
				sy := bounds.Min.Y + y*bounds.Dy()/300
				canvas.Set((i%3)*300+xx, (i/3)*300+y, pic.At(sx, sy))
			}
		}
	}
	var buf bytes.Buffer
	if err = png.Encode(&buf, canvas); err != nil {
		featureError(w, err)
		return
	}
	// Library covers use the existing covers table and work in compatible clients.
	_, err = a.db.Exec("INSERT INTO covers(id,mime,data) VALUES(?,?,?) ON CONFLICT(id) DO UPDATE SET mime=excluded.mime,data=excluded.data", request.Library, "image/png", buf.Bytes())
	if err != nil {
		featureError(w, err)
		return
	}
	respond(w, M{"ok": true, "Library": request.Library})
}
