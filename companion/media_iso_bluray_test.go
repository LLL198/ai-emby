package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
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
			if !input.native.timeSeek {
				t.Fatal("time navigation protocol missing")
			}
			_, indexed := hlsPoints(ctx, input.native, 12)
			t.Logf("authored title duration %.3fs, safe time index=%v", float64(input.native.duration)/90000, indexed)
			for _, tick := range []uint64{0, input.native.duration / 2} {
				offset, actual, err := input.native.seekTime(tick)
				if err != nil && !indexed {
					// tsMuxeR's short split titles can contain an unusable time index.
					// It must be rejected while the READ protocol remains usable below.
					t.Logf("unsafe time point rejected at %d ticks", tick)
					continue
				}
				if err != nil || offset < 0 || offset >= input.native.size || actual > input.native.duration {
					t.Fatalf("native time seek: offset=%d time=%d err=%v", offset, actual, err)
				}
			}
			if _, _, err := input.native.seekTime(input.native.duration); err == nil {
				t.Fatal("EOF time accepted")
			}
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

func TestISONativeTimeSeekProtocolAndFailureRecovery(t *testing.T) {
	inputReader, inputWriter := io.Pipe()
	outputReader, outputWriter := io.Pipe()
	defer inputReader.Close()
	defer inputWriter.Close()
	defer outputReader.Close()
	defer outputWriter.Close()
	reader := &isoNativeReader{ctx: context.Background(), control: newPlaybackTaskControl(), input: inputWriter, output: outputReader, timeSeek: true, duration: 1080000, size: 512}
	helperDone := make(chan error, 1)
	go func() {
		for step := 0; step < 3; step++ {
			var request [16]byte
			if _, err := io.ReadFull(inputReader, request[:]); err != nil {
				helperDone <- err
				return
			}
			var reply [24]byte
			length := 8
			if step < 2 {
				expected := uint64(90000)
				if step == 1 {
					expected = 810000
				}
				if string(request[:4]) != "SEEK" || binary.BigEndian.Uint32(request[4:8]) != 0 || binary.BigEndian.Uint64(request[8:]) != expected {
					helperDone <- fmt.Errorf("invalid SEEK request at step %d", step)
					outputWriter.Close()
					return
				}
				if step == 0 {
					binary.BigEndian.PutUint32(reply[4:8], 16)
					binary.BigEndian.PutUint64(reply[8:16], 64)
					binary.BigEndian.PutUint64(reply[16:], 90000)
					length = 24
				} else {
					binary.BigEndian.PutUint32(reply[:4], 1)
				}
			} else {
				if string(request[:4]) != "READ" || binary.BigEndian.Uint32(request[4:8]) != 4 || binary.BigEndian.Uint64(request[8:]) != 0 {
					helperDone <- fmt.Errorf("READ did not follow failed SEEK")
					outputWriter.Close()
					return
				}
				binary.BigEndian.PutUint32(reply[4:8], 4)
				copy(reply[8:12], "data")
				length = 12
			}
			if _, err := outputWriter.Write(reply[:length]); err != nil {
				helperDone <- err
				return
			}
		}
		helperDone <- nil
	}()
	if offset, actual, err := reader.seekTime(90000); err != nil || offset != 64 || actual != 90000 {
		t.Fatalf("successful SEEK decoded incorrectly: %d %d %v", offset, actual, err)
	}
	if _, _, err := reader.seekTime(810000); err == nil {
		t.Fatal("failed SEEK accepted")
	}
	var data [4]byte
	if n, err := reader.ReadAt(data[:], 0); err != nil || n != 4 || string(data[:]) != "data" {
		t.Fatalf("READ failed after rejected SEEK: %d %v", n, err)
	}
	if err := <-helperDone; err != nil {
		t.Fatal(err)
	}
}
