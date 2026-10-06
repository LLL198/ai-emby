package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

func TestISONativeBluRayRangeSeekAndCancel(t *testing.T) {
	root := os.Getenv("ISO_NATIVE_FIXTURE_ROOT")
	if root == "" {
		t.Skip("ISO_NATIVE_FIXTURE_ROOT required for libbluray integration checks")
	}
	for _, name := range []string{"native-udf250.iso", "native-angles.iso"} {
		t.Run(name, func(t *testing.T) {
			file, err := os.Open(filepath.Join(root, name))
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			info, _ := file.Stat()
			var whole atomic.Int32
			var block atomic.Bool
			blocked, upstreamCanceled := make(chan struct{}, 1), make(chan struct{}, 1)
			unblock := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Range") == "" {
					whole.Add(1)
					http.Error(w, "whole-image download forbidden", 500)
					return
				}
				if block.Load() {
					select {
					case blocked <- struct{}{}:
					default:
					}
					select {
					case <-r.Context().Done():
						select {
						case upstreamCanceled <- struct{}{}:
						default:
						}
						return
					case <-unblock:
					}
				}
				http.ServeContent(w, r, name, time.Time{}, io.NewSectionReader(file, 0, info.Size()))
			}))
			defer server.Close()
			defer close(unblock)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			control := newPlaybackTaskControl()
			metadataCtx, metadataCancel := playbackTaskTimeout(ctx, control, 30*time.Second)
			defer metadataCancel()
			source := newISORangeReader(metadataCtx, server.URL, info.Size())
			source.control = control
			input, err := prepareISOBluRay(ctx, metadataCtx, source, info.Size(), control)
			if err != nil {
				t.Fatal(err)
			}
			defer input.close()
			source.startStreaming(ctx)
			metadataCancel()
			referenceRequest, _ := http.NewRequest("GET", input.URL, nil)
			referenceRequest.Header.Set("Range", "bytes=0-8388607")
			referenceResponse, err := http.DefaultClient.Do(referenceRequest)
			if err != nil {
				t.Fatal(err)
			}
			reference, err := io.ReadAll(referenceResponse.Body)
			referenceResponse.Body.Close()
			if err != nil || referenceResponse.StatusCode != 206 || len(reference) != 8<<20 {
				t.Fatalf("sequential reference: bytes=%d err=%v", len(reference), err)
			}
			frame := exec.Command("ffmpeg", "-v", "error", "-f", "mpegts", "-i", "pipe:0", "-map", "0:v:0", "-frames:v", "1", "-f", "hash", "-hash", "sha256", "-")
			frame.Stdin = bytes.NewReader(reference)
			actualFrame, err := frame.Output()
			if err != nil {
				t.Fatal(err)
			}
			expectedFrame, err := exec.Command("ffmpeg", "-v", "error", "-i", filepath.Join(root, "source.mkv"), "-map", "0:v:0", "-frames:v", "1", "-f", "hash", "-hash", "sha256", "-").Output()
			if err != nil || !bytes.Equal(actualFrame, expectedFrame) {
				t.Fatalf("default angle changed the first frame: err=%v", err)
			}
			read := func(offset int64) []byte {
				t.Helper()
				request, _ := http.NewRequest("GET", input.URL, nil)
				request.Header.Set("Range", "bytes="+strconv.FormatInt(offset, 10)+"-"+strconv.FormatInt(offset+65535, 10))
				response, err := http.DefaultClient.Do(request)
				if err != nil {
					t.Fatal(err)
				}
				defer response.Body.Close()
				data, err := io.ReadAll(response.Body)
				if err != nil || response.StatusCode != 206 || len(data) != 65536 {
					t.Fatalf("native Range: %d bytes=%d err=%v", response.StatusCode, len(data), err)
				}
				return data
			}
			first := read(0)
			if first[4] != 0x47 {
				t.Fatal("native reader did not provide M2TS packets")
			}
			if err := control.change("pause"); err != nil {
				t.Fatal(err)
			}
			pausedRead := make(chan []byte, 1)
			go func() { pausedRead <- read(192*4001 + 17) }()
			select {
			case <-pausedRead:
				t.Fatal("native movie read continued while paused")
			case <-time.After(150 * time.Millisecond):
			}
			if err := control.change("resume"); err != nil {
				t.Fatal(err)
			}
			select {
			case data := <-pausedRead:
				if !bytes.Equal(data, reference[192*4001+17:192*4001+17+65536]) {
					t.Fatal("resuming changed movie bytes")
				}
			case <-time.After(2 * time.Second):
				t.Fatal("native movie read did not resume")
			}
			for _, offset := range []int64{192*4001 + 17, 7<<20 + 13, 0} {
				a := read(offset)
				b := read(offset)
				if !bytes.Equal(a, b) || !bytes.Equal(a, reference[offset:offset+65536]) ||
					(offset == 0 && !bytes.Equal(a, first)) {
					t.Fatalf("seeking changed movie bytes at %d", offset)
				}
			}
			if whole.Load() != 0 {
				t.Fatal("native reader downloaded the complete ISO")
			}
			// Cancel while the helper is waiting on an upstream read, rather than only when idle.
			source.startStreaming(ctx)
			block.Store(true)
			readDone := make(chan struct{})
			go func() {
				defer close(readDone)
				if response, err := http.Get(input.URL); err == nil {
					_, _ = io.Copy(io.Discard, response.Body)
					response.Body.Close()
				}
			}()
			select {
			case <-blocked:
			case <-time.After(3 * time.Second):
				t.Fatal("native reader did not reach the blocked source")
			}
			cancel()
			closed := make(chan struct{})
			go func() { input.close(); close(closed) }()
			for _, done := range []<-chan struct{}{closed, readDone, upstreamCanceled} {
				select {
				case <-done:
				case <-time.After(2 * time.Second):
					t.Fatal("cancel did not stop the helper and its upstream read")
				}
			}
			requestCtx, requestCancel := context.WithTimeout(context.Background(), time.Second)
			defer requestCancel()
			request, _ := http.NewRequestWithContext(requestCtx, "GET", input.URL, nil)
			if response, err := http.DefaultClient.Do(request); err == nil {
				response.Body.Close()
				t.Fatal("canceled native reader left its listener running")
			}
		})
	}
}
