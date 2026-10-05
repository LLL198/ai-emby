package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func isoFeatureRequest(a *App, w http.ResponseWriter, r *http.Request) {
	user, err := a.auth(r)
	if err != nil {
		fail(w, 401, "unauthorized")
		return
	}
	a.featureRoute(w, r, user)
}

func TestISOActualFormatDetection(t *testing.T) {
	disc := make([]byte, isoHeaderSize)
	copy(disc[32769:], "BEA01")
	for _, tc := range []struct {
		name string
		data []byte
		want string
	}{
		{"renamed MKV", append([]byte{0x1a, 0x45, 0xdf, 0xa3}, []byte("matroska")...), "mkv"},
		{"WebM", append([]byte{0x1a, 0x45, 0xdf, 0xa3}, []byte("webm")...), "webm"},
		{"renamed MP4", []byte{0, 0, 0, 24, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm'}, "mp4"},
		{"real UDF", disc, "iso"},
		{"truncated", []byte{0x1a, 0x45}, "unknown"},
		{"not a disc", []byte("<html>login required</html>"), "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := mediaHeaderContainer(tc.data); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
	for _, x := range []Item{
		{Path: "/media/movie.ISO"},
		{Path: "/media/movie.strm", URL: "https://cloud.example/cloud/resolve/m?path=%2Fmovie.iso&sign=private"},
		{URL: "https://cdn.example/movie.iso?signature=private"},
	} {
		if !isoMediaCandidate(x) {
			t.Fatal("missed original ISO filename")
		}
	}
	if isoMediaCandidate(Item{URL: "https://cloud.example/movie.mkv?name=wrong.iso"}) {
		t.Fatal("unrelated query misidentified an ISO")
	}
}

func TestISOInspectionUsesBoundedRange(t *testing.T) {
	header := make([]byte, isoHeaderSize)
	copy(header, []byte{0x1a, 0x45, 0xdf, 0xa3})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") != "bytes=0-65535" {
			t.Error("inspection fetched the complete movie")
		}
		w.Header().Set("Content-Range", "bytes 0-65535/5000000000")
		w.WriteHeader(http.StatusPartialContent)
		w.Write(header)
	}))
	defer server.Close()
	info, err := inspectISOInput(context.Background(), server.URL+"/source.iso")
	if err != nil || info.Container != "mkv" || info.Size != 5000000000 {
		t.Fatalf("inspection: %+v, %v", info, err)
	}
}

func TestISORenamedLocalVideoDoesNotUseBluray(t *testing.T) {
	root := t.TempDir()
	t.Setenv("MEDIA_ROOTS", root)
	file := filepath.Join(root, "movie.iso")
	if err := os.WriteFile(file, []byte{0x1a, 0x45, 0xdf, 0xa3}, 0600); err != nil {
		t.Fatal(err)
	}
	var a App
	input, err := a.featureMediaInput(Item{Path: file})
	if err != nil || input != file {
		t.Fatalf("renamed MKV sent to optical-disc reader: %q %v", input, err)
	}
}

func TestISOCacheRejectsPartialAndOversizeDownloads(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "8")
		w.Write([]byte("short"))
	}))
	defer server.Close()
	target := filepath.Join(t.TempDir(), "source.iso")
	noop := func(int64, int64) {}
	if err := downloadISO(context.Background(), server.URL, target, 8, 4, noop); err == nil {
		t.Fatal("oversize cache accepted")
	}
	if err := downloadISO(context.Background(), server.URL, target, 8, 100, noop); err == nil {
		t.Fatal("truncated ISO accepted")
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("partial image reusable as complete")
	}
	if _, err := os.Stat(target + ".partial"); !os.IsNotExist(err) {
		t.Fatal("failed download was not cleaned up")
	}
}

func TestISODVDTitleSelectionAndBluRayPlaylist(t *testing.T) {
	listing := "Path = VIDEO_TS/VTS_01_0.VOB\nSize = 999999\n\nPath = VIDEO_TS/VTS_01_2.VOB\nSize = 80\n\nPath = VIDEO_TS/VTS_01_1.VOB\nSize = 100\n\nPath = VIDEO_TS/VTS_02_1.VOB\nSize = 50\n\nPath = ../outside.mkv\nSize = 999999\n\n"
	files, bluray := isoMainFiles(isoArchiveFiles(listing))
	if bluray || len(files) != 2 || files[0].Path != "VIDEO_TS/VTS_01_1.VOB" || files[1].Path != "VIDEO_TS/VTS_01_2.VOB" {
		t.Fatalf("wrong DVD title/segment ordering: %+v", files)
	}
	_, bluray = isoMainFiles([]isoArchiveFile{{Path: "BDMV/STREAM/00001.m2ts", Size: 1024}})
	if !bluray {
		t.Fatal("Blu-ray playlist replaced by biggest-clip guessing")
	}
}

func TestISOInspectionRequiresSessionAndLibraryAccess(t *testing.T) {
	a, _, source, calls := cloudProtectionFixture(t)
	iso := strings.Replace(source, "movie.mkv", "movie.iso", 1)
	a.db.Exec("UPDATE items SET url=? WHERE id='movie'", iso)
	for _, query := range []string{"", "?api_key=not-a-session"} {
		w := httptest.NewRecorder()
		isoFeatureRequest(a, w, httptest.NewRequest("GET", "/features/playback-inspect"+query, nil))
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("anonymous inspection: %d", w.Code)
		}
	}
	a.db.Exec("INSERT INTO feature_library_policy(lib,data) VALUES('lib',?)", `{"RestrictUsers":true,"Users":[]}`)
	w := httptest.NewRecorder()
	isoFeatureRequest(a, w, httptest.NewRequest("GET", "/features/playback-inspect?ID=movie&api_key=viewer-token", nil))
	if w.Code != 404 || *calls != 0 {
		t.Fatalf("restricted source inspected: %d, calls %d", w.Code, *calls)
	}
}

func TestISOPlaybackStatusDoesNotExposeOtherUsersOrSources(t *testing.T) {
	a := testApp(t)
	a.features.transcodes = map[string]*featureTranscode{"job": {Owner: "other", Item: "movie", Directory: "/private/source", State: "caching"}}
	w := httptest.NewRecorder()
	a.featurePlaybackStatus(w, httptest.NewRequest("GET", "/features/playback-status?ID=job", nil), User{ID: "viewer"})
	if w.Code != 404 || strings.Contains(w.Body.String(), "/private/") {
		t.Fatal("another user's cache status leaked")
	}
	if !a.features.transcodes["job"].Used.IsZero() {
		t.Fatal("another user extended the cache lifetime")
	}
}

func TestISORealPlaybackFixtures(t *testing.T) {
	root := os.Getenv("ISO_FIXTURE_ROOT")
	if root == "" {
		t.Skip("ISO_FIXTURE_ROOT required for real FFmpeg/disc integration checks")
	}
	for _, tc := range []struct {
		name    string
		noRange bool
		large   bool
		start   int
	}{
		{"renamed-mkv.iso", false, false, 0}, {"dvd.iso", false, false, 0}, {"video-disc.iso", false, false, 0}, {"dvd.iso", true, false, 0},
		{"bluray.iso", false, false, 0}, {"video-disc.iso", false, true, 0}, {"bluray.iso", false, false, 5},
	} {
		name := tc.name
		t.Run(name+"/range="+strconv.FormatBool(!tc.noRange)+"/large="+strconv.FormatBool(tc.large)+"/start="+strconv.Itoa(tc.start), func(t *testing.T) {
			a, _, _, _ := cloudProtectionFixture(t)
			a.features.ctx = context.Background()
			a.features.transcodes = map[string]*featureTranscode{}
			t.Cleanup(func() { a.stopFeatures(); a.features.wg.Wait() })
			if err := a.saveFeatureSetting("playback", featurePlaybackConfig{Transcode: true, Threads: 2, Concurrency: 2, CacheGB: 1, Bitrate: 1000, RetentionDays: 1}); err != nil {
				t.Fatal(err)
			}
			var fullRequests atomic.Int32
			fileServer := http.FileServer(http.Dir(root))
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.large {
					if r.Header.Get("Range") == "" {
						fullRequests.Add(1)
						http.Error(w, "whole-image download forbidden", 500)
						return
					}
					file, err := os.Open(filepath.Join(root, name))
					if err != nil {
						http.Error(w, "missing fixture", 500)
						return
					}
					defer file.Close()
					info, _ := file.Stat()
					virtual := &isoJoinedReader{size: 2 << 30, files: []isoStreamFile{{size: info.Size(), reader: file}, {size: (2 << 30) - info.Size(), reader: isoZeroReader{}}}}
					http.ServeContent(w, r, name, time.Time{}, io.NewSectionReader(virtual, 0, virtual.size))
					return
				}
				if r.Header.Get("Range") == "" {
					fullRequests.Add(1)
				}
				if tc.noRange && r.Header.Get("Range") != "bytes=0-65535" {
					r.Header.Del("Range")
				}
				fileServer.ServeHTTP(w, r)
			}))
			defer server.Close()
			if _, err := a.db.Exec("UPDATE items SET url=? WHERE id='movie'", server.URL+"/"+name); err != nil {
				t.Fatal(err)
			}
			inspection := httptest.NewRecorder()
			isoFeatureRequest(a, inspection, httptest.NewRequest("GET", "/features/playback-inspect?ID=movie&api_key=viewer-token", nil))
			if inspection.Code != 200 {
				t.Fatalf("inspection: %d %s", inspection.Code, inspection.Body)
			}
			w := httptest.NewRecorder()
			isoFeatureRequest(a, w, httptest.NewRequest("POST", "/features/playback?api_key=viewer-token", strings.NewReader(`{"ID":"movie","Audio":-1,"Start":`+strconv.Itoa(tc.start)+`}`)))
			if w.Code != 200 {
				t.Fatalf("playback: %d %s", w.Code, w.Body)
			}
			var result struct{ ID, URL string }
			if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			deadline := time.After(20 * time.Second)
			for {
				a.features.cacheMu.Lock()
				job := *a.features.transcodes[result.ID]
				a.features.cacheMu.Unlock()
				if job.Done {
					if job.Error != "" {
						t.Fatal(job.Error)
					}
					probe, err := transferProbe(context.Background(), filepath.Join(job.Directory, "video.mp4"))
					if err != nil || len(probe.Streams) < 2 || probe.Streams[0].CodecName != "h264" || probe.Streams[1].CodecName != "aac" {
						t.Fatalf("not a browser-compatible movie: %+v %v", probe, err)
					}
					duration, _ := strconv.ParseFloat(probe.Format.Duration, 64)
					if duration < float64(11-tc.start) || duration > float64(14-tc.start) {
						t.Fatalf("positive video segments were dropped: duration %g", duration)
					}
					if name != "renamed-mkv.iso" && !tc.noRange {
						if job.SourceMode != "range" || fullRequests.Load() != 0 {
							t.Fatalf("image was downloaded instead of read on demand: mode=%s full=%d", job.SourceMode, fullRequests.Load())
						}
						if _, err := os.Stat(filepath.Join(job.Directory, "source.iso")); !os.IsNotExist(err) {
							t.Fatal("full image stored for online playback")
						}
					}
					if tc.noRange {
						if job.SourceMode != "cache" {
							t.Fatal("unsupported ranges did not fall back to full cache")
						}
						// A retry must reuse the already complete image and extracted title even with no free cache.
						filler := filepath.Join(job.Directory, "cache-budget-test")
						file, err := os.Create(filler)
						if err != nil {
							t.Fatal(err)
						}
						remaining := int64(1<<30) - featureDirectorySize(filepath.Join(featureDataRoot(), "playback"))
						truncateErr := file.Truncate(remaining)
						file.Close()
						if truncateErr != nil {
							t.Fatal(truncateErr)
						}
						info, err := inspectISOInput(context.Background(), server.URL+"/"+name)
						if err != nil {
							t.Fatal(err)
						}
						_, err = a.prepareISOPlayback(context.Background(), server.URL+"/"+name, job.Directory, info, &featureTranscode{})
						os.Remove(filler)
						if err != nil {
							t.Fatalf("completed disc cache was not reusable: %v", err)
						}
					}
					stream := httptest.NewRecorder()
					request := httptest.NewRequest("GET", result.URL+"?api_key=viewer-token", nil)
					request.Header.Set("Range", "bytes=0-1023")
					isoFeatureRequest(a, stream, request)
					if stream.Code != 206 || stream.Body.Len() != 1024 {
						t.Fatalf("cached video range: %d %d", stream.Code, stream.Body.Len())
					}
					break
				}
				select {
				case <-deadline:
					t.Fatal("disc playback did not finish")
				case <-time.After(100 * time.Millisecond):
				}
			}
		})
	}
}
