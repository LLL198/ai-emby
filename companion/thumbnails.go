package main

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

type thumbnailEntry struct {
	data []byte
	mime string
}

var thumbnailCache = struct {
	sync.Mutex
	entries map[string]thumbnailEntry
	size    int
}{entries: make(map[string]thumbnailEntry)}
var thumbnailSlots = make(chan struct{}, 2)

// Bounded JPEG thumbnails for native clients. Original PNG/WebP stays available
// when no resize is requested; transparent artwork is kept in PNG format.
func serveThumbnail(w http.ResponseWriter, r *http.Request, src io.ReadSeeker) bool {
	width, _ := strconv.Atoi(q(r, "MaxWidth"))
	height, _ := strconv.Atoi(q(r, "MaxHeight"))
	if width <= 0 {
		width, _ = strconv.Atoi(q(r, "Width"))
	}
	if height <= 0 {
		height, _ = strconv.Atoi(q(r, "Height"))
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	for i, p := range parts {
		if strings.EqualFold(p, "Images") && len(parts) > i+7 {
			if width <= 0 {
				width, _ = strconv.Atoi(parts[i+5])
			}
			if height <= 0 {
				height, _ = strconv.Atoi(parts[i+6])
			}
			break
		}
	}
	if width <= 0 && height <= 0 {
		return false
	}
	width = min(max(width, 0), 4096)
	height = min(max(height, 0), 4096)
	cacheKey := ""
	if tag := w.Header().Get("ETag"); tag != "" {
		cacheKey = fmt.Sprintf("%s:%d:%d", tag, width, height)
	}
	serveCached := func() bool {
		if cacheKey == "" {
			return false
		}
		thumbnailCache.Lock()
		v, ok := thumbnailCache.entries[cacheKey]
		thumbnailCache.Unlock()
		if ok {
			w.Header().Set("Content-Type", v.mime)
			http.ServeContent(w, r, "thumbnail", time.Time{}, bytes.NewReader(v.data))
		}
		return ok
	}
	if serveCached() {
		return true
	}
	select {
	case thumbnailSlots <- struct{}{}:
		defer func() { <-thumbnailSlots }()
	case <-r.Context().Done():
		return true
	}
	if serveCached() {
		return true
	}
	cfg, format, e := image.DecodeConfig(src)
	src.Seek(0, io.SeekStart)
	if e != nil || (format != "jpeg" && format != "png") || cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > 25000000 {
		return false
	}
	scale := 1.0
	if width > 0 {
		scale = math.Min(scale, float64(width)/float64(cfg.Width))
	}
	if height > 0 {
		scale = math.Min(scale, float64(height)/float64(cfg.Height))
	}
	if scale >= 1 {
		return false
	}
	img, _, e := image.Decode(src)
	src.Seek(0, io.SeekStart)
	if e != nil {
		return false
	}
	dw := max(1, int(float64(cfg.Width)*scale))
	dh := max(1, int(float64(cfg.Height)*scale))
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	bounds := img.Bounds()
	for y := 0; y < dh; y++ {
		fy := float64(y) * float64(cfg.Height) / float64(dh)
		y0 := int(fy)
		ty := fy - float64(y0)
		y1 := min(y0+1, cfg.Height-1)
		for x := 0; x < dw; x++ {
			fx := float64(x) * float64(cfg.Width) / float64(dw)
			x0 := int(fx)
			tx := fx - float64(x0)
			x1 := min(x0+1, cfg.Width-1)
			c00 := color.RGBAModel.Convert(img.At(bounds.Min.X+x0, bounds.Min.Y+y0)).(color.RGBA)
			c10 := color.RGBAModel.Convert(img.At(bounds.Min.X+x1, bounds.Min.Y+y0)).(color.RGBA)
			c01 := color.RGBAModel.Convert(img.At(bounds.Min.X+x0, bounds.Min.Y+y1)).(color.RGBA)
			c11 := color.RGBAModel.Convert(img.At(bounds.Min.X+x1, bounds.Min.Y+y1)).(color.RGBA)
			mix := func(a, b, c, d uint8) uint8 {
				return uint8((float64(a)*(1-tx)+float64(b)*tx)*(1-ty) + (float64(c)*(1-tx)+float64(d)*tx)*ty + 0.5)
			}
			dst.SetRGBA(x, y, color.RGBA{mix(c00.R, c10.R, c01.R, c11.R), mix(c00.G, c10.G, c01.G, c11.G), mix(c00.B, c10.B, c01.B, c11.B), mix(c00.A, c10.A, c01.A, c11.A)})
		}
	}
	var out bytes.Buffer
	if format == "png" {
		if e := png.Encode(&out, dst); e != nil {
			return false
		}
		w.Header().Set("Content-Type", "image/png")
	} else {
		if e := jpeg.Encode(&out, dst, &jpeg.Options{Quality: 85}); e != nil {
			return false
		}
		w.Header().Set("Content-Type", "image/jpeg")
	}
	if cacheKey != "" && out.Len() <= 1<<20 {
		thumbnailCache.Lock()
		for thumbnailCache.size+out.Len() > 32<<20 {
			for key, v := range thumbnailCache.entries {
				delete(thumbnailCache.entries, key)
				thumbnailCache.size -= len(v.data)
				break
			}
		}
		if previous, ok := thumbnailCache.entries[cacheKey]; ok {
			thumbnailCache.size -= len(previous.data)
		}
		thumbnailCache.entries[cacheKey] = thumbnailEntry{out.Bytes(), w.Header().Get("Content-Type")}
		thumbnailCache.size += out.Len()
		thumbnailCache.Unlock()
	}
	http.ServeContent(w, r, "thumbnail", time.Time{}, bytes.NewReader(out.Bytes()))
	return true
}
