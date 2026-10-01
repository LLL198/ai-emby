package main

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
)

type scanFile struct {
	Path, Root string
	Stamp      fileStamp
}
type scanInventory struct {
	Snapshot fileSnapshot
	Files    []scanFile
	Regular  map[string]bool
	Children map[string][]string
}

func (a *App) inventoryForScan(ctx context.Context, job string, roots, scopes []string) (*scanInventory, error) {
	inventory := &scanInventory{Snapshot: fileSnapshot{}, Regular: map[string]bool{}, Children: map[string][]string{}}
	for _, root := range roots {
		if info, err := os.Stat(root); err != nil || !info.IsDir() {
			return nil, fmt.Errorf("媒体目录不可用，保留原索引")
		}
		targets := []string{root}
		if len(scopes) > 0 {
			targets = nil
			for _, scope := range scopes {
				if scope == root || strings.HasPrefix(scope, root+string(filepath.Separator)) {
					targets = append(targets, scope)
				}
			}
		}
		for _, target := range targets {
			if _, err := os.Lstat(target); os.IsNotExist(err) && len(scopes) > 0 {
				continue
			}
			err := boundedWalk(target, func(path string, entry fs.DirEntry, walkErr error) error {
				if walkErr != nil {
					return walkErr
				}
				if err := a.waitConcurrentScan(ctx, job); err != nil {
					return err
				}
				if entry.Type()&os.ModeSymlink != 0 {
					if entry.IsDir() {
						return filepath.SkipDir
					}
					return nil
				}
				if entry.IsDir() {
					if path != root && strings.HasPrefix(entry.Name(), ".") {
						return filepath.SkipDir
					}
					inventory.Children[filepath.Dir(path)] = append(inventory.Children[filepath.Dir(path)], path)
					return nil
				}
				info, err := entry.Info()
				if err != nil {
					return err
				}
				if _, seen := inventory.Snapshot[path]; seen {
					return nil
				}
				stamp := fileStamp{Size: info.Size(), Mtime: info.ModTime().UnixNano()}
				inventory.Snapshot[path] = stamp
				inventory.Regular[path] = info.Mode().IsRegular() && info.Size() > 0
				if info.Mode().IsRegular() && featureMediaExtension(path) {
					inventory.Files = append(inventory.Files, scanFile{Path: path, Root: root, Stamp: stamp})
				}
				return nil
			})
			if err != nil {
				return nil, err
			}
		}
	}
	for directory := range inventory.Children {
		sort.Strings(inventory.Children[directory])
	}
	return inventory, nil
}

type scanNFO struct {
	sidecar
	Year    int `xml:"year"`
	Season  int `xml:"season"`
	Episode int `xml:"episode"`
}
type scanNFOEntry struct {
	once  sync.Once
	data  scanNFO
	valid bool
}
type scanRegularEntry struct {
	once    sync.Once
	regular bool
}
type scanParentEntry struct {
	once  sync.Once
	value parsedScanItem
}
type scanSeriesEntry struct {
	once sync.Once
	path string
}
type scanImageEntry struct {
	once sync.Once
	path string
}
type scanCache struct {
	ctx           context.Context
	inventory     *scanInventory
	nfos          sync.Map
	regular       sync.Map
	parents       sync.Map
	series        sync.Map
	images        sync.Map
	seasonNumbers map[string]int
}

func (cache *scanCache) safeImage(path string) string {
	value, _ := cache.images.LoadOrStore(path, &scanImageEntry{})
	entry := value.(*scanImageEntry)
	entry.once.Do(func() {
		if cache.ctx.Err() == nil && cache.isRegular(path) {
			entry.path = safeImage(path)
		}
	})
	return entry.path
}

func newScanCache(ctx context.Context, inventory *scanInventory) *scanCache {
	cache := &scanCache{ctx: ctx, inventory: inventory, seasonNumbers: map[string]int{}}
	// Establish season fallbacks in inventory order before workers run.
	for _, file := range inventory.Files {
		directory := filepath.Dir(file.Path)
		if _, exists := cache.seasonNumbers[directory]; exists {
			continue
		}
		season := 0
		if match := epPattern.FindStringSubmatch(filepath.Base(file.Path)); len(match) == 3 {
			season, _ = strconv.Atoi(match[1])
		}
		cache.seasonNumbers[directory] = season
	}
	return cache
}

func (cache *scanCache) isRegular(path string) bool {
	if regular, known := cache.inventory.Regular[path]; known {
		return regular
	}
	value, _ := cache.regular.LoadOrStore(path, &scanRegularEntry{})
	entry := value.(*scanRegularEntry)
	entry.once.Do(func() {
		if cache.ctx.Err() == nil {
			info, err := os.Stat(path)
			entry.regular = err == nil && info.Mode().IsRegular() && info.Size() > 0
		}
	})
	return entry.regular
}

func (cache *scanCache) nfo(path string) (scanNFO, bool) {
	value, _ := cache.nfos.LoadOrStore(path, &scanNFOEntry{})
	entry := value.(*scanNFOEntry)
	entry.once.Do(func() {
		if cache.ctx.Err() != nil || !cache.isRegular(path) {
			return
		}
		real, err := filepath.EvalSymlinks(path)
		if err != nil || !allowedMediaPath(real) {
			return
		}
		file, err := os.Open(real)
		if err != nil {
			return
		}
		defer file.Close()
		decoder := xml.NewDecoder(io.LimitReader(file, 2<<20))
		entry.valid = decoder.Decode(&entry.data) == nil
	})
	return entry.data, entry.valid
}

func (cache *scanCache) applyInfo(item *Item) {
	base := strings.TrimSuffix(item.Path, filepath.Ext(item.Path))
	directory := filepath.Dir(item.Path)
	paths := []string{base + ".nfo", filepath.Join(directory, "movie.nfo"), filepath.Join(directory, "tvshow.nfo")}
	if item.Kind == "Episode" {
		paths = []string{base + ".nfo"}
	}
	if item.Kind == "Series" {
		directory = item.Path
		paths = []string{filepath.Join(directory, "tvshow.nfo")}
	}
	if item.Kind == "Season" {
		directory = item.Path
		paths = []string{filepath.Join(directory, "season.nfo"), filepath.Join(directory, "movie.nfo"), filepath.Join(directory, "tvshow.nfo")}
	}
	for _, path := range paths {
		if meta, valid := cache.nfo(path); valid {
			if meta.Title != "" {
				item.Name = meta.Title
			}
			item.Overview, item.Year = meta.Plot, meta.Year
			if meta.Season > 0 {
				item.Season = meta.Season
			}
			if meta.Episode > 0 {
				item.Episode = meta.Episode
			}
			break
		}
	}
	for _, extension := range []string{".jpg", ".png", ".webp", ".jpeg"} {
		candidates := []string{base + "-poster" + extension, base + extension, filepath.Join(directory, "poster"+extension), filepath.Join(directory, "folder"+extension)}
		if item.Kind == "Series" || item.Kind == "Season" {
			candidates = []string{filepath.Join(directory, "poster"+extension), filepath.Join(directory, "folder"+extension)}
		}
		for _, path := range candidates {
			if cache.isRegular(path) {
				item.Poster = path
				return
			}
		}
	}
}

func (cache *scanCache) sidecar(item Item) sidecar {
	base := strings.TrimSuffix(item.Path, filepath.Ext(item.Path))
	paths := []string{base + ".nfo", filepath.Join(filepath.Dir(item.Path), "movie.nfo")}
	if item.Kind == "Episode" {
		paths = []string{base + ".nfo"}
	}
	if item.Kind == "Series" {
		paths = []string{filepath.Join(item.Path, "tvshow.nfo")}
	}
	if item.Kind == "Season" {
		paths = []string{filepath.Join(item.Path, "season.nfo"), filepath.Join(filepath.Dir(item.Path), "tvshow.nfo")}
	}
	for _, path := range paths {
		if meta, valid := cache.nfo(path); valid {
			return meta.sidecar
		}
	}
	return sidecar{}
}

func (cache *scanCache) seriesDirectory(root, mediaPath string) string {
	directory := filepath.Dir(mediaPath)
	value, _ := cache.series.LoadOrStore(root+"\x00"+directory, &scanSeriesEntry{})
	entry := value.(*scanSeriesEntry)
	entry.once.Do(func() {
		for parent := directory; parent != root && parent != "/" && parent != "."; parent = filepath.Dir(parent) {
			if seasonDirPattern.MatchString(filepath.Base(parent)) {
				entry.path = filepath.Dir(parent)
				return
			}
			if cache.isRegular(filepath.Join(parent, "tvshow.nfo")) {
				entry.path = parent
				return
			}
		}
		relative, _ := filepath.Rel(root, directory)
		entry.path = filepath.Join(root, strings.Split(relative, string(filepath.Separator))[0])
	})
	return entry.path
}

func (cache *scanCache) seasonPoster(path string, season int) string {
	for _, extension := range []string{".jpg", ".png", ".webp", ".jpeg"} {
		for _, candidate := range []string{filepath.Join(path, "poster"+extension), filepath.Join(path, "folder"+extension), filepath.Join(filepath.Dir(path), fmt.Sprintf("season%02d-poster%s", season, extension)), filepath.Join(filepath.Dir(path), fmt.Sprintf("season%d-poster%s", season, extension))} {
			if cache.isRegular(candidate) {
				return candidate
			}
		}
	}
	return ""
}

func (cache *scanCache) parent(lib, parent, path, kind string) parsedScanItem {
	value, _ := cache.parents.LoadOrStore(kind+"\x00"+path, &scanParentEntry{})
	entry := value.(*scanParentEntry)
	entry.once.Do(func() {
		item := Item{ID: digest(path)[:32], Lib: lib, Parent: parent, Name: filepath.Base(path), Kind: kind, Path: path}
		if kind == "Season" {
			item.Season = cache.seasonNumbers[path]
			if match := seasonDirPattern.FindStringSubmatch(filepath.Base(path)); len(match) == 2 {
				item.Season, _ = strconv.Atoi(match[1])
			}
			item.Episode = item.Season
		}
		cache.applyInfo(&item)
		if kind == "Season" {
			item.Poster = cache.seasonPoster(path, item.Season)
		}
		if kind == "Series" && item.Poster == "" {
			for _, child := range cache.inventory.Children[path] {
				if match := seasonDirPattern.FindStringSubmatch(filepath.Base(child)); len(match) == 2 {
					season, _ := strconv.Atoi(match[1])
					if poster := cache.seasonPoster(child, season); poster != "" {
						item.Poster = poster
						break
					}
				}
			}
		}
		entry.value = parsedScanItem{Item: item, Metadata: cache.sidecar(item)}
	})
	return entry.value
}
