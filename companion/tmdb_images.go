package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"image"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const maxArtworkBytes = 20 << 20

var posterHTTP = &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(r *http.Request, via []*http.Request) error {
	if len(via) > 2 || r.URL.Scheme != "https" || r.URL.Host != "image.tmdb.org" {
		return fmt.Errorf("image redirect rejected")
	}
	return nil
}}

type artworkGate struct {
	gate chan struct{}
	refs int
}

var (
	artworkGatesMu sync.Mutex
	artworkGates   = make(map[string]*artworkGate)

	validatedArtworkMu sync.Mutex
	validatedArtwork   = make(map[[32]byte]struct{})
)

// lockTMDBImage serializes requests for the same URL and lets waiting callers
// leave when their context is canceled.
func lockTMDBImage(ctx context.Context, key string) (func(), bool) {
	if ctx == nil {
		return nil, false
	}

	artworkGatesMu.Lock()
	entry := artworkGates[key]
	if entry == nil {
		entry = &artworkGate{gate: make(chan struct{}, 1)}
		artworkGates[key] = entry
	}
	entry.refs++
	artworkGatesMu.Unlock()

	select {
	case entry.gate <- struct{}{}:
		var once sync.Once
		return func() {
			once.Do(func() {
				<-entry.gate
				artworkGatesMu.Lock()
				entry.refs--
				if entry.refs == 0 && artworkGates[key] == entry {
					delete(artworkGates, key)
				}
				artworkGatesMu.Unlock()
			})
		}, true
	case <-ctx.Done():
		artworkGatesMu.Lock()
		entry.refs--
		if entry.refs == 0 && artworkGates[key] == entry {
			delete(artworkGates, key)
		}
		artworkGatesMu.Unlock()
		return nil, false
	}
}

func readArtworkCache(path string) ([]byte, bool) {
	file, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxArtworkBytes {
		return nil, false
	}
	data, err := io.ReadAll(io.LimitReader(file, maxArtworkBytes+1))
	if err != nil || len(data) == 0 || len(data) > maxArtworkBytes {
		return nil, false
	}
	if err := scraperValidateImage(data); err != nil {
		return nil, false
	}

	digest := sha256.Sum256(data)
	validatedArtworkMu.Lock()
	_, decoded := validatedArtwork[digest]
	validatedArtworkMu.Unlock()
	if !decoded {
		if _, _, err := image.Decode(bytes.NewReader(data)); err != nil {
			return nil, false
		}
		validatedArtworkMu.Lock()
		if len(validatedArtwork) > 0x3ff {
			clear(validatedArtwork)
		}
		validatedArtwork[digest] = struct{}{}
		validatedArtworkMu.Unlock()
	}
	return data, true
}

func writeArtworkCache(path string, data []byte) {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o770); err != nil {
		return
	}
	temporary := filepath.Join(directory, ".image-"+id())
	file, err := os.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o664)
	if err != nil {
		return
	}
	defer os.Remove(temporary)

	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return
	}
	if err := file.Close(); err != nil {
		return
	}
	_ = os.Rename(temporary, path)
}

func (a *App) serveTMDBImage(w http.ResponseWriter, r *http.Request, itemID, rawURL string) {
	requestPath := strings.TrimPrefix(r.URL.Path, "/emby")
	_, kind, _ := imageRequest(requestPath)
	if strings.EqualFold(kind, "Primary") || strings.EqualFold(kind, "Thumb") {
		rawURL = strings.Replace(rawURL, "/original/", "/w500/", 1)
	}

	settings := a.tmdbSettings()
	cachePath := filepath.Join(settings.Directory, "images", digest(rawURL)+".img")
	serve := func(data []byte) {
		w.Header().Set("Content-Type", http.DetectContentType(data))
		reader := bytes.NewReader(data)
		if serveThumbnail(w, r, reader) {
			return
		}
		http.ServeContent(w, r, "artwork", time.Time{}, reader)
	}

	if data, ok := readArtworkCache(cachePath); ok {
		serve(data)
		return
	}
	release, ok := lockTMDBImage(r.Context(), rawURL)
	if !ok {
		return
	}
	defer release()
	if data, ok := readArtworkCache(cachePath); ok {
		serve(data)
		return
	}

	request, err := http.NewRequestWithContext(r.Context(), http.MethodGet, rawURL, nil)
	if err != nil {
		fail(w, http.StatusBadGateway, "图片地址无效")
		return
	}
	response, err := a.externalHTTPClient("tmdb", posterHTTP).Do(request)
	if err != nil {
		a.markActorImageFailure(itemID, rawURL)
		fail(w, http.StatusBadGateway, "图片暂不可用")
		return
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.HasPrefix(response.Header.Get("Content-Type"), "image/") {
		a.markActorImageFailure(itemID, rawURL)
		fail(w, http.StatusBadGateway, "图片暂不可用")
		return
	}

	data, err := io.ReadAll(io.LimitReader(response.Body, 5<<20+1))
	if err != nil || len(data) > 5<<20 {
		fail(w, http.StatusBadGateway, "图片过大")
		return
	}
	if err := scraperValidateImage(data); err != nil {
		fail(w, http.StatusBadGateway, "图片内容无效")
		return
	}
	writeArtworkCache(cachePath, data)
	serve(data)
}
