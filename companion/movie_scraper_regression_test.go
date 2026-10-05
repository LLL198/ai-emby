package main

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestMovieSequelRecognitionRetainsNumber(t *testing.T) {
	for _, title := range []string{"真人快打2", "Mortal Kombat 2", "金刚大战哥斯拉2"} {
		if got := scraperTitleVariants(title); !reflect.DeepEqual(got, []string{title}) {
			t.Errorf("sequel search broadened: %q => %q", title, got)
		}
	}
	candidate := scraperTMDBCandidate{ID: 931285, Title: "真人快打2", OriginalTitle: "Mortal Kombat II", ReleaseDate: "2026-05-06", MediaType: "movie"}
	if score := scraperCandidateScore("movie", "真人快打2", 2026, 0, 0, candidate); score < 80 {
		t.Errorf("exact sequel rejected: score %d", score)
	}
	wrong := scraperTMDBCandidate{ID: 3, Title: "真人快打3", OriginalTitle: "Mortal Kombat 3", ReleaseDate: "2026-01-01", MediaType: "movie"}
	if _, err := scraperSelectCandidate("movie", []scraperTMDBCandidate{wrong}, "真人快打2", 2026, 0, 0); err == nil {
		t.Error("different sequel accepted solely by year")
	}
	if _, _, err := namingSelectTMDB([]scraperTMDBCandidate{wrong}, "真人快打2", 2026, true); err == nil {
		t.Error("different sequel accepted for renaming solely by year")
	}
}

func TestMovieArtworkPermanentFailureAndCancellation(t *testing.T) {
	for _, code := range []int{http.StatusNotFound, http.StatusForbidden, http.StatusOK} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.WriteHeader(code)
				w.Write([]byte("not an image"))
			}))
			defer server.Close()
			a := &App{}
			if _, err := a.scraperDownloadArtwork(context.Background(), server.URL+"/poster?token=private", t.TempDir()); err == nil || strings.Contains(err.Error(), "private") || calls.Load() != 1 {
				t.Fatalf("permanent failure retried or leaked: %v; calls %d", err, calls.Load())
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
		cancel()
	}))
	defer server.Close()
	if _, err := (&App{}).scraperDownloadArtwork(ctx, server.URL, t.TempDir()); !errors.Is(err, context.Canceled) || calls.Load() != 1 {
		t.Fatalf("cancellation retried: %v, %d", err, calls.Load())
	}
}

func TestMovieArtworkQueueCancelsWithoutUsingDownloadBudget(t *testing.T) {
	releases := []func(){}
	defer func() {
		for _, release := range releases {
			release()
		}
	}()
	for i := 0; i < maxScraperArtworkConcurrency; i++ {
		release, ok := acquireScraperArtwork(context.Background())
		if !ok {
			t.Fatal("could not fill artwork slots")
		}
		releases = append(releases, release)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if release, ok := acquireScraperArtwork(ctx); ok {
		release()
		t.Fatal("exceeded artwork concurrency")
	}
}

func TestMovieArtworkRetriesHeaderTimeout(t *testing.T) {
	var pngData bytes.Buffer
	if err := png.Encode(&pngData, image.NewRGBA(image.Rect(0, 0, 2, 3))); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) <= 2 {
			<-r.Context().Done()
			return
		}
		w.Write(pngData.Bytes())
	}))
	defer server.Close()
	previous := posterHTTP
	client := *posterHTTP
	client.Timeout = 40 * time.Millisecond
	posterHTTP = &client
	t.Cleanup(func() { posterHTTP = previous })
	data, err := (&App{}).scraperDownloadArtwork(context.Background(), server.URL+"/poster", t.TempDir())
	if err != nil || !bytes.Equal(data, pngData.Bytes()) || calls.Load() != 3 {
		t.Fatal("header timeout did not recover", err, calls.Load())
	}
}

func TestMovieRenameApplyPreservesSTRMAndCatalog(t *testing.T) {
	a := testApp(t)
	if _, err := a.db.DB.Exec(featureSchema); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	t.Setenv("FILE_MANAGER_ROOT", root)
	t.Setenv("FILE_BUSY_SOCKET", "")
	folder := filepath.Join("电影", "真人快打2 {tmdb-931285}")
	old := filepath.Join(folder, "真人快打2.2026.strm")
	if err := os.MkdirAll(filepath.Join(root, folder), 0755); err != nil {
		t.Fatal(err)
	}
	strm := []byte("https://example.invalid/video?signature=unchanged\n")
	for path, data := range map[string][]byte{
		old: strm,
		strings.TrimSuffix(old, ".strm") + ".nfo": []byte(`<movie><title>真人快打2</title><year>2026</year><tmdbid>931285</tmdbid></movie>`),
	} {
		if err := os.WriteFile(filepath.Join(root, path), data, 0644); err != nil {
			t.Fatal(err)
		}
	}
	var searches atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/movie/931285" {
			searches.Add(1)
			w.WriteHeader(404)
			return
		}
		json.NewEncoder(w).Encode(tmdbData{ID: 931285, Title: "真人快打2", OriginalTitle: "Mortal Kombat II", ReleaseDate: "2026-05-06"})
	}))
	defer server.Close()
	settings := tmdbConfig{Enabled: true, APIBase: server.URL, APIKey: "test-key", Directory: t.TempDir(), RequestsPerSecond: 100}
	raw, _ := json.Marshal(settings)
	if _, err := a.db.Exec("INSERT INTO settings(k,v) VALUES('tmdb',?)", string(raw)); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec("INSERT INTO libraries(id,name,path,kind) VALUES('lib','电影',?,'movies')", filepath.Join(root, "电影")); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec("INSERT INTO items(id,lib,parent,name,kind,path,url,seen) VALUES('movie','lib','lib','真人快打2','Movie',?,?, 'g')", filepath.Join(root, old), strings.TrimSpace(string(strm))); err != nil {
		t.Fatal(err)
	}
	var user User
	if err := a.db.QueryRow("SELECT id FROM users LIMIT 1").Scan(&user.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec("INSERT INTO userdata(user_id,item,position,played) VALUES(?,'movie',12345,1)", user.ID); err != nil {
		t.Fatal(err)
	}
	request, _ := json.Marshal(namingRequest{Path: "电影", Mode: "auto", Kind: "movie", Recursive: true, Folders: true})
	w := httptest.NewRecorder()
	a.namingPreview(w, httptest.NewRequest("POST", "/admin/features/naming/preview", bytes.NewReader(request)), user)
	if w.Code != 200 {
		t.Fatal("preview", w.Code, w.Body.String())
	}
	var plan namingPlan
	if err := json.Unmarshal(w.Body.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	selected := []string{}
	for _, row := range plan.Rows {
		if row.Status == "review" || row.Status == "conflict" {
			t.Fatal("unexpected review", row)
		}
		if row.Status == "ready" {
			selected = append(selected, row.ID)
		}
	}
	if len(selected) != 2 || searches.Load() != 0 {
		t.Fatal("folder and file not resolved using existing ID", selected, searches.Load())
	}
	// Inspect migrated catalog before the normal follow-up scan starts.
	a.scanner.locks = a.db.DB
	request, _ = json.Marshal(M{"id": plan.ID, "rows": selected})
	w = httptest.NewRecorder()
	a.namingApply(w, httptest.NewRequest("POST", "/admin/features/naming/apply", bytes.NewReader(request)), user)
	if w.Code != 200 {
		t.Fatal("apply", w.Code, w.Body.String())
	}
	next := filepath.Join(root, "电影", "真人快打2 (2026) {tmdb-931285}", "真人快打2 (2026) {tmdb-931285}.strm")
	data, err := os.ReadFile(next)
	if err != nil || !bytes.Equal(data, strm) {
		t.Fatal("STRM link changed or destination missing", err)
	}
	if _, err := os.Stat(strings.TrimSuffix(next, ".strm") + ".nfo"); err != nil {
		t.Fatal("NFO not migrated", err)
	}
	var position, played int64
	if err := a.db.QueryRow("SELECT d.position,d.played FROM userdata d JOIN items i ON i.id=d.item WHERE i.path=?", next).Scan(&position, &played); err != nil || position != 12345 || played != 1 {
		t.Fatal("watch history lost", position, played, err)
	}
}

func TestMoviePartialScrapePreservesNFOAndCorrectYear(t *testing.T) {
	a := testApp(t)
	if _, err := a.db.DB.Exec(featureSchema); err != nil {
		t.Fatal(err)
	}
	a.features.ctx = context.Background()
	a.scraper.running = true
	t.Cleanup(func() {
		a.refreshGuard.mu.Lock()
		defer a.refreshGuard.mu.Unlock()
		for _, timer := range a.refreshGuard.timers {
			timer.Stop()
		}
	})
	root := t.TempDir()
	t.Setenv("FILE_MANAGER_ROOT", root)
	path := filepath.Join(root, "真人快打2 (2026) {tmdb-931285}", "真人快打2.2026.strm")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("https://example.invalid/video"), 0644); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/images") {
			io.WriteString(w, `{"posters":[{"file_path":"/test.jpg","iso_639_1":"zh"}]}`)
			return
		}
		io.WriteString(w, `{"id":931285,"title":"真人快打2","original_title":"Mortal Kombat II","release_date":"2025-05-06","original_language":"en","poster_path":"/test.jpg"}`)
	}))
	defer server.Close()
	settings := tmdbConfig{Enabled: true, APIBase: server.URL, Directory: t.TempDir(), RequestsPerSecond: 100}
	raw, _ := json.Marshal(settings)
	if _, err := a.db.Exec("INSERT INTO settings(k,v) VALUES('tmdb',?)", string(raw)); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec("INSERT INTO libraries(id,name,path,kind) VALUES('lib','电影',?,'movies')", root); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec("INSERT INTO items(id,lib,parent,name,kind,path,url,year,seen) VALUES('movie','lib','lib','真人快打2','Movie',?,'https://example.invalid/video',2026,'g')", path); err != nil {
		t.Fatal(err)
	}
	item, err := a.item("movie")
	if err != nil {
		t.Fatal(err)
	}
	previous := posterHTTP
	var calls atomic.Int32
	posterHTTP = &http.Client{Transport: cloudProtectionTransport(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{StatusCode: 503, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
	})}
	t.Cleanup(func() { posterHTTP = previous })
	object := scraperObject{ID: item.ID, Item: item, Kind: item.Kind, Name: item.Name, Targets: []scraperTarget{
		{Content: "NFO", Path: scraperFilename("NFO", item.Kind, path), Action: "create"},
		{Content: "Poster", Path: scraperFilename("Poster", item.Kind, path), Action: "create"},
	}}
	err = a.scrapeObject(context.Background(), scraperConfig{ChineseMetadata: true}, object, tmdbScraper{}, a.newActivity("scraper", item.ID, item.Name))
	if err == nil || !strings.Contains(err.Error(), "部分刮削失败") || calls.Load() != 3 {
		t.Fatal("partial result incorrect", err, calls.Load())
	}
	data, err := os.ReadFile(scraperFilename("NFO", item.Kind, path))
	var nfo tmdbNFORecord
	if err != nil || xml.Unmarshal(data, &nfo) != nil || nfo.Year != 2025 || nfo.TMDBID != "931285" {
		t.Fatal("NFO removed or release year incorrect", err, nfo.Year, nfo.TMDBID)
	}
	var reason string
	if err := a.db.QueryRow("SELECT reason FROM feature_media_issues WHERE source='scraper'").Scan(&reason); err != nil || !strings.Contains(reason, "部分刮削失败") {
		t.Fatal("partial issue not persisted", err, reason)
	}
}

func TestMovieParentIdentityIsSharedByNamingAndScraper(t *testing.T) {
	path := filepath.Join("电影", "真人快打2 {tmdb-931285}", "真人快打2.2026.strm")
	source, err := namingSource(path, false, namingRequest{Mode: "auto", Kind: "movie"}, 0)
	if err != nil || source.Identity.TMDBID != "931285" || source.Identity.Year != 2026 {
		t.Errorf("parent movie ID lost: %+v, %v", source.Identity, err)
	}
	_, endpoint := tmdbIdentityWith(Item{ID: "movie", Kind: "Movie", Path: path, Name: "真人快打2.2026"}, nil, func(Item) sidecar { return sidecar{} })
	if endpoint != "movie/931285" {
		t.Errorf("scraper ignored parent movie ID: %s", endpoint)
	}
	for _, request := range []namingRequest{
		{Mode: "auto", Kind: "movie", Title: "另一个电影", Year: 2027},
		{Mode: "auto", Kind: "movie", Year: 2027},
	} {
		source, err := namingSource(path, false, request, 0)
		if err != nil || source.Identity.TMDBID != "" || source.Identity.Year != 2027 {
			t.Errorf("explicit correction inherited stale directory ID: %+v, %v", source.Identity, err)
		}
	}
	for _, path := range []string{
		filepath.Join("电影", "无关电影 (2026) {tmdb-999}", "真人快打2.2026.strm"),
		filepath.Join("电影", "真人快打3 (2026) {tmdb-999}", "真人快打2.2026.strm"),
	} {
		source, err := namingSource(path, false, namingRequest{Mode: "auto", Kind: "movie"}, 0)
		if err != nil || source.Identity.TMDBID != "" {
			t.Errorf("unrelated directory ID inherited: %+v, %v", source.Identity, err)
		}
	}
}

func TestMovieArtworkRetriesTransientFailuresAndCaches(t *testing.T) {
	var pngData bytes.Buffer
	if err := png.Encode(&pngData, image.NewRGBA(image.Rect(0, 0, 2, 3))); err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) <= 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Write(pngData.Bytes())
	}))
	defer server.Close()
	a := &App{}
	directory := t.TempDir()
	for i := 0; i < 2; i++ {
		data, err := a.scraperDownloadArtwork(context.Background(), server.URL+"/poster.png", directory)
		if err != nil || !bytes.Equal(data, pngData.Bytes()) {
			t.Fatalf("transient artwork failure not recovered: %v", err)
		}
	}
	if requests.Load() != 3 {
		t.Fatalf("cache or retry count incorrect: %d", requests.Load())
	}
}
