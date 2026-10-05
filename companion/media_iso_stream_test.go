package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
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
