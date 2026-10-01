package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
)

// A nil file scope includes every item.
func (scope *scraperFileScope) contains(libraryID, path string) bool {
	if scope == nil {
		return true
	}
	if scope.Library != libraryID {
		return false
	}
	relative, err := filepath.Rel(scope.Path, path)
	if err != nil {
		return false
	}
	if relative == "." {
		return true
	}
	return scope.Directory && filepath.IsLocal(relative)
}

// scraperItemScope captures the library root and the item's root-relative path
// after verifying the path remains inside the configured library.
func (a *App) scraperItemScope(libraryID, path string) (*scraperFileScope, error) {
	libraryRoot, err := a.scraperRoot(libraryID, path)
	if err != nil {
		return nil, err
	}
	root, relative, err := scraperOpen(libraryRoot, path)
	if err != nil {
		return nil, err
	}
	defer root.Close()

	info, err := root.Stat(relative)
	if err != nil {
		return nil, err
	}
	return &scraperFileScope{
		Library:   libraryID,
		Root:      libraryRoot,
		Relative:  relative,
		Path:      path,
		Directory: info.IsDir(),
	}, nil
}

// scraperFileScope resolves a file-manager path against the current library
// configuration and returns the most specific matching library location.
func (a *App) scraperFileScope(path string) (*scraperFileScope, error) {
	return a.scraperFileScopeIn(path, a.libraries())
}

func (a *App) scraperFileScopeIn(path string, libraries []M) (*scraperFileScope, error) {
	name, err := fileName(path)
	if err != nil {
		return nil, err
	}

	managerRoot := os.Getenv("FILE_MANAGER_ROOT")
	if managerRoot == "" {
		managerRoot = "/media"
	}
	root, err := os.OpenRoot(managerRoot)
	if err != nil {
		return nil, err
	}
	defer root.Close()

	info, err := fileCheck(root, name)
	if err != nil {
		return nil, err
	}
	absolutePath := filepath.Join(managerRoot, name)
	isDirectory := info.IsDir()

	var best *scraperFileScope
	for _, library := range libraries {
		libraryID := library["Id"].(string)
		locations := library["Locations"].([]string)
		for _, location := range locations {
			relative, err := filepath.Rel(location, absolutePath)
			if err != nil || (relative != "." && !filepath.IsLocal(relative)) {
				continue
			}

			libraryRoot, err := scraperLibraryRoot(location)
			if err != nil {
				continue
			}
			_, checkErr := fileCheck(libraryRoot, relative)
			_ = libraryRoot.Close()
			if checkErr != nil {
				continue
			}

			if !isDirectory {
				var count int
				if err := a.db.QueryRow(
					"SELECT count(*) FROM items WHERE lib=? AND path=? AND kind IN ('Movie','Episode')",
					libraryID,
					absolutePath,
				).Scan(&count); err != nil || count == 0 {
					continue
				}
			}

			if best != nil && len(best.Root) >= len(location) {
				continue
			}
			best = &scraperFileScope{
				Library:   libraryID,
				Root:      location,
				Relative:  relative,
				Path:      absolutePath,
				Directory: isDirectory,
			}
		}
	}
	if best == nil {
		return nil, errors.New("目标不是已配置媒体库中的目录或媒体")
	}
	return best, nil
}

// waitScraper waits for resume or cancellation through the caller context.
func (a *App) waitScraper(ctx context.Context, activityID string) error {
	for {
		a.scraper.mu.Lock()
		pause := a.scraper.pause
		a.scraper.mu.Unlock()
		if pause == nil {
			return ctx.Err()
		}

		a.scraperWaitState(activityID, "paused")
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-pause:
		}
		a.scraperWaitState(activityID, "running")
	}
}

// Update the task and active scraper activities together.
func (a *App) scraperWaitState(activityID, state string) {
	activeTask := a.scraper.taskID.Load()
	activeTaskID := ""
	if activeTask != nil {
		activeTaskID = *activeTask
	}

	a.activity.mu.Lock()
	defer a.activity.mu.Unlock()
	for _, entry := range a.activity.entries {
		if entry.ID != activityID && !(entry.Category == "scraper" && activeTaskID != "" && entry.ItemID == activeTaskID) {
			continue
		}
		entry.State = state
	}
}

func (a *App) scraperControl(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		fail(w, http.StatusMethodNotAllowed, "PUT required")
		return
	}
	var request struct {
		Paused bool
	}
	if !body(w, r, &request) {
		return
	}

	a.scraper.mu.Lock()
	if !a.scraper.running {
		a.scraper.mu.Unlock()
		fail(w, http.StatusConflict, "没有正在运行的刮削任务")
		return
	}
	if request.Paused {
		if a.scraper.pause == nil {
			a.scraper.pause = make(chan struct{})
		}
	} else if a.scraper.pause != nil {
		close(a.scraper.pause)
		a.scraper.pause = nil
	}
	paused := a.scraper.pause != nil
	a.scraper.mu.Unlock()

	respond(w, M{"Paused": paused})
}
