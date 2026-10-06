package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

type isoZeroReader struct{}

func (isoZeroReader) ReadAt(data []byte, offset int64) (int, error) {
	clear(data)
	return len(data), nil
}

func TestISORangeReaderBoundaryCacheAndCancellation(t *testing.T) {
	data := bytes.Repeat([]byte("range-data"), isoRangeBlock)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.ServeContent(w, r, "disc.iso", time.Time{}, bytes.NewReader(data))
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reader := newISORangeReader(ctx, server.URL, int64(len(data)))
	buffer := make([]byte, 32)
	offset := int64(isoRangeBlock - 8)
	if n, err := reader.ReadAt(buffer, offset); n != len(buffer) || err != nil || !bytes.Equal(buffer, data[offset:offset+32]) {
		t.Fatalf("cross-block read corrupted: %d %v", n, err)
	}
	if n, err := reader.ReadAt(buffer, offset); n != len(buffer) || err != nil || calls != 2 {
		t.Fatalf("cached sectors fetched again: %d %v calls=%d", n, err, calls)
	}
	cancel()
	if _, err := reader.ReadAt(buffer, offset); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled job kept reading its source")
	}
}

func TestISORangeReaderRejectsIgnoredOrWrongRanges(t *testing.T) {
	for _, header := range []string{"", "bytes 1-262144/524288", "bytes 0-262143/999999"} {
		t.Run(header, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if header != "" {
					w.Header().Set("Content-Range", header)
					w.WriteHeader(206)
				}
				w.Write([]byte("wrong"))
			}))
			defer server.Close()
			reader := newISORangeReader(context.Background(), server.URL, 524288)
			if _, err := reader.ReadAt(make([]byte, 16), 0); !errors.Is(err, errISORangeUnavailable) {
				t.Fatalf("unreliable range accepted: %v", err)
			}
		})
	}
}

func TestISORangeReaderStreamsLargeBlocksWithoutReusingMetadata(t *testing.T) {
	data := bytes.Repeat([]byte("stream-data"), (3*isoStreamBlock+4096)/len("stream-data")+1)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.ServeContent(w, r, "disc.iso", time.Time{}, bytes.NewReader(data))
	}))
	defer server.Close()
	metadataCtx, cancel := context.WithCancel(context.Background())
	reader := newISORangeReader(metadataCtx, server.URL, int64(len(data)))
	if _, err := reader.ReadAt(make([]byte, 16), 0); err != nil {
		t.Fatal(err)
	}
	reader.startStreaming(context.Background())
	cancel()
	buffer := make([]byte, 32<<10)
	// A metadata-sized cache entry at zero used to cause a zero-copy loop after a block-size change.
	for offset := int64(0); offset < 3*isoStreamBlock; offset += int64(len(buffer)) {
		if n, err := reader.ReadAt(buffer, offset); err != nil || n != len(buffer) || !bytes.Equal(buffer, data[offset:offset+int64(n)]) {
			t.Fatalf("movie read at %d: %d %v", offset, n, err)
		}
	}
	if calls.Load() != 4 {
		t.Fatalf("12 MiB should need three movie requests and one metadata request, got %d", calls.Load())
	}
	var cached int
	for _, entry := range reader.blocks {
		cached += len(entry.Value.(isoRangeCacheBlock).data)
	}
	if cached > isoRangeCacheBytes {
		t.Fatalf("movie memory cache exceeded limit: %d", cached)
	}
	// Probe/seeking can revisit an evicted block or the short final block.
	for _, offset := range []int64{0, int64(len(data) - len(buffer))} {
		if n, err := reader.ReadAt(buffer, offset); err != nil || !bytes.Equal(buffer[:n], data[offset:offset+int64(n)]) {
			t.Fatalf("seek at %d: %d %v", offset, n, err)
		}
	}
}

func TestISOJoinedReaderAndPrivateListener(t *testing.T) {
	movie := &isoJoinedReader{size: 10, files: []isoStreamFile{
		{size: 4, reader: bytes.NewReader([]byte("abcd"))},
		{size: 6, reader: bytes.NewReader([]byte("EFGHIJ"))},
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	input, err := serveISOOnline(ctx, movie, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer input.close()
	request, _ := http.NewRequest("GET", input.URL, nil)
	request.Header.Set("Range", "bytes=2-6")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != 206 || string(data) != "cdEFG" {
		t.Fatalf("joined movie range: %d %q", response.StatusCode, data)
	}
	response, err = http.Get(input.URL + "/private-source")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 404 {
		t.Fatal("listener accepted an arbitrary media path")
	}
	input.close()
	if response, err := http.Get(input.URL); err == nil {
		response.Body.Close()
		t.Fatal("completed job left a playback listener open")
	}
}

func isoTestPlaylist(clips ...string) []byte {
	data := make([]byte, 50+36*len(clips))
	copy(data, "MPLS0200")
	binary.BigEndian.PutUint32(data[8:], 40)
	binary.BigEndian.PutUint32(data[40:], uint32(len(data)-44))
	binary.BigEndian.PutUint16(data[46:], uint16(len(clips)))
	for i, clip := range clips {
		position := 50 + 36*i
		binary.BigEndian.PutUint16(data[position:], 34)
		item := data[position+2:]
		copy(item, clip)
		copy(item[5:], "M2TS")
		item[10] = 1
		binary.BigEndian.PutUint32(item[12:], 600*45000)
		binary.BigEndian.PutUint32(item[16:], 606*45000)
	}
	return data
}

func TestISOBluRayPlaylistOrderTimesAndUnsupportedNavigation(t *testing.T) {
	files := map[string]isoStreamFile{
		"BDMV/STREAM/00001.M2TS": {name: "BDMV/STREAM/00001.m2ts", size: 1000},
		"BDMV/STREAM/00002.M2TS": {name: "BDMV/STREAM/00002.m2ts", size: 999999},
	}
	main := isoTestPlaylist("00001", "00002")
	short := isoTestPlaylist("00002")
	files["BDMV/PLAYLIST/00001.MPLS"] = isoStreamFile{name: "BDMV/PLAYLIST/00001.mpls", size: int64(len(main)), reader: bytes.NewReader(main)}
	files["BDMV/PLAYLIST/00002.MPLS"] = isoStreamFile{name: "BDMV/PLAYLIST/00002.mpls", size: int64(len(short)), reader: bytes.NewReader(short)}
	clips, err := isoBluRayMain(files)
	if err != nil || len(clips) != 2 || clips[0].file.name != "BDMV/STREAM/00001.m2ts" || clips[0].in != 600 || clips[1].out != 606 {
		t.Fatalf("wrong main playlist: %+v %v", clips, err)
	}
	for _, change := range []func([]byte){
		func(data []byte) { data[62] |= 0x10 },                                 // multiple angles
		func(data []byte) { data[49] = 1 },                                     // subpath
		func(data []byte) { data[52] = '/' },                                   // invalid clip identifier
		func(data []byte) { binary.BigEndian.PutUint32(data[8:], 0xffffffff) }, // invalid playlist offset
		func(data []byte) { binary.BigEndian.PutUint32(data[68:], 599*45000) }, // out before in
	} {
		data := append([]byte(nil), main...)
		change(data)
		if _, _, err := isoBluRayPlaylist(data, "BDMV", files); err == nil {
			t.Fatal("unsupported playlist silently played as a different movie")
		}
	}
}

func TestISOBluRayMainIgnoresMenusAndKeepsLongestTitle(t *testing.T) {
	main := isoTestPlaylist("00001", "00002")
	menu := isoTestPlaylist("00001")
	menu[81] = 1 // still_mode in the first play item
	files := map[string]isoStreamFile{
		"BDMV/STREAM/00001.M2TS": {name: "BDMV/STREAM/00001.m2ts", size: 1000},
		"BDMV/STREAM/00002.M2TS": {name: "BDMV/STREAM/00002.m2ts", size: 1000},
	}
	add := func(name string, data []byte) {
		files[name] = isoStreamFile{name: name, size: int64(len(data)), reader: bytes.NewReader(data)}
	}
	add("BDMV/PLAYLIST/00000.MPLS", []byte("broken menu"))
	add("BDMV/PLAYLIST/00001.MPLS", menu)
	add("BDMV/PLAYLIST/00002.MPLS", main)
	clips, err := isoBluRayMain(files)
	if err != nil || len(clips) != 2 {
		t.Fatalf("an unrelated menu prevented main-title playback: %+v %v", clips, err)
	}
	// A complex main title must still fall back, instead of silently playing a shorter trailer.
	binary.BigEndian.PutUint32(menu[68:], 630*45000)
	add("BDMV/PLAYLIST/00001.MPLS", menu)
	if clips, err := isoBluRayMain(files); err == nil || len(clips) != 0 {
		t.Fatalf("a short trailer replaced the unsupported main title: %+v %v", clips, err)
	}
	// Alternate editions of the same duration can use a supported primary playlist.
	binary.BigEndian.PutUint32(menu[68:], 612*45000)
	add("BDMV/PLAYLIST/00001.MPLS", menu)
	if clips, err := isoBluRayMain(files); err != nil || len(clips) != 2 {
		t.Fatalf("supported equal-duration title was rejected: %+v %v", clips, err)
	}
}
