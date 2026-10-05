package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFullMediaExtractionFields(t *testing.T) {
	a := testApp(t)
	x := probeFixture(t, a)
	bin := t.TempDir()
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	script := `#!/bin/sh
printf '%s' '{"format":{"duration":"60","format_name":"matroska","size":"1400000"},"streams":[{"index":0,"codec_type":"video","codec_name":"hevc","width":1440,"height":1080,"avg_frame_rate":"30000/1001","profile":"Main","level":120,"display_aspect_ratio":"4:3","field_order":"progressive","pix_fmt":"yuv420p","color_transfer":"bt709"},{"index":1,"codec_type":"audio","codec_name":"ac3","channel_layout":"stereo","channels":2,"bit_rate":"187000","sample_rate":"48000","disposition":{"default":1}}]}'
`
	if err := os.WriteFile(filepath.Join(bin, "ffprobe"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	j := a.queueProbe(x, "t", "test", false)
	select {
	case <-j.done:
	case <-time.After(3 * time.Second):
		t.Fatal("probe stalled")
	}
	if j.err != nil {
		t.Fatal(j.err)
	}
	streams := j.data["MediaStreams"].([]M)
	v := streams[0]
	audio := streams[1]
	for key, want := range (M{"DisplayTitle": "1080p HEVC", "Codec": "hevc", "Width": 1440, "Height": 1080, "VideoRange": "SDR", "Profile": "Main", "Level": float64(120), "AspectRatio": "4:3", "IsInterlaced": false, "BitDepth": 8, "PixelFormat": "yuv420p"}) {
		if v[key] != want {
			t.Errorf("%s: %v want %v", key, v[key], want)
		}
	}
	if rate := v["AverageFrameRate"].(float64); rate < 29.97 || rate > 29.971 {
		t.Fatal(rate)
	}
	for key, want := range (M{"ChannelLayout": "stereo", "Channels": 2, "Codec": "ac3", "BitRate": 187000, "SampleRate": 48000, "IsDefault": true, "IsExternal": false}) {
		if audio[key] != want {
			t.Errorf("%s: %v", key, audio[key])
		}
	}
	if j.data["Partial"] == true {
		t.Fatal("partial extraction")
	}
}
