package photos

import (
	"context"
	"encoding/binary"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestBrowsersPlayVideosTheCheapestWayTheyCan(t *testing.T) {
	iphone := videoInfo{Container: "mov,mp4,m4a,3gp,3g2,mj2", Codec: "hevc", PixelFormat: "yuv420p10le", Audio: "aac"}
	android := videoInfo{Container: "mov,mp4,m4a,3gp,3g2,mj2", Codec: "h264", PixelFormat: "yuv420p", Audio: "aac"}
	camcorder := videoInfo{Container: "mpegts", Codec: "h264", PixelFormat: "yuv420p", Audio: "ac3"}
	oldCamera := videoInfo{Container: "avi", Codec: "mjpeg", PixelFormat: "yuvj422p", Audio: "pcm_s16le"}
	webm := videoInfo{Container: "matroska,webm", Codec: "vp9", Audio: "opus"}
	mkv := videoInfo{Container: "matroska,webm", Codec: "h264", PixelFormat: "yuv420p", Audio: "aac"}
	for _, test := range []struct {
		name string
		info videoInfo
		caps BrowserCaps
		mode string
		mime string
	}{
		{"HEVC where the browser decodes it", iphone, BrowserCaps{HEVC: true}, PlayDirect, ""},
		{"HEVC where it does not", iphone, BrowserCaps{}, PlayTranscode, `video/mp4; codecs="avc1.640033,mp4a.40.2"`},
		{"H.264 in MP4", android, BrowserCaps{}, PlayDirect, ""},
		{"a camcorder's transport stream", camcorder, BrowserCaps{}, PlayRemux, `video/mp4; codecs="avc1.640033,mp4a.40.2"`},
		{"a camcorder's stream where AC-3 plays", camcorder, BrowserCaps{AC3: true}, PlayRemux, `video/mp4; codecs="avc1.640033,mp4a.40.2"`},
		{"an old camera's AVI", oldCamera, BrowserCaps{HEVC: true}, PlayTranscode, `video/mp4; codecs="avc1.640033,mp4a.40.2"`},
		{"WebM", webm, BrowserCaps{VP9: true, WebM: true}, PlayDirect, ""},
		{"Matroska in Chromium", mkv, BrowserCaps{Matroska: true}, PlayDirect, ""},
		{"Matroska elsewhere", mkv, BrowserCaps{}, PlayRemux, `video/mp4; codecs="avc1.640033,mp4a.40.2"`},
		{"silent HEVC copied", videoInfo{Container: "mpegts", Codec: "hevc"}, BrowserCaps{HEVC: true}, PlayRemux, `video/mp4; codecs="hvc1.1.6.L153.B0"`},
	} {
		t.Run(test.name, func(t *testing.T) {
			mode, request := decideVideo(test.info, test.caps)
			if mode != test.mode {
				t.Fatalf("mode = %s; want %s", mode, test.mode)
			}
			if mode == PlayDirect {
				return
			}
			if mime := streamMime(test.info, request); mime != test.mime {
				t.Fatalf("mime = %s; want %s", mime, test.mime)
			}
			if mode == PlayRemux && (!request.CopyVideo || request.Height != 0) || mode == PlayTranscode && (request.CopyVideo || request.Height != transcodeHeight) {
				t.Fatalf("request = %+v for %s", request, mode)
			}
			if request.CopyAudio != (test.info.Audio == "aac") {
				t.Fatalf("copy audio = %v for %s", request.CopyAudio, test.info.Audio)
			}
		})
	}
}

func TestVideoContainersAreToldApartByTheirFirstBytes(t *testing.T) {
	box := func(kind, brand string) []byte {
		b := make([]byte, sniffHeader)
		binary.BigEndian.PutUint32(b, 24)
		copy(b[4:], kind+brand)
		return b
	}
	transport := make([]byte, sniffHeader)
	transport[0], transport[188] = 0x47, 0x47
	m2ts := make([]byte, sniffHeader)
	m2ts[4], m2ts[196] = 0x47, 0x47
	for header, want := range map[string]string{
		string(box("ftyp", "qt  ")):                                "video/quicktime",
		string(box("ftyp", "isom")):                                "video/mp4",
		string(box("ftyp", "mp42")):                                "video/mp4",
		string(box("ftyp", "3gp4")):                                "video/3gpp",
		string(box("ftyp", "heic")):                                "image/heic",
		string(box("ftyp", "avif")):                                "",
		string(box("ftyp", "crx ")):                                "",
		string(box("moov", "")):                                    "video/quicktime",
		string(box("wide", "")):                                    "video/quicktime",
		"\x1a\x45\xdf\xa3\x9f\x42\x86\x81\x01\x42\x82\x84webm":     "video/webm",
		"\x1a\x45\xdf\xa3\xa3\x42\x86\x81\x01\x42\x82\x88matroska": "video/x-matroska",
		"RIFF\x00\x00\x00\x00AVI LIST":                             "video/x-msvideo",
		"RIFF\x00\x00\x00\x00WAVEfmt ":                             "",
		string(transport):                                          "video/mp2t",
		string(m2ts):                                               "video/mp2t",
		"GIF89a":                                                   "",
	} {
		if got := sniffMediaType([]byte(header)); got != want {
			t.Errorf("sniffMediaType(%q) = %q; want %q", header[:min(len(header), 16)], got, want)
		}
	}
}

func TestConvertingVideosWaitBrieflyForAFreeSlot(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "photos"), Options{DisableCapacityReserve: true})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.streamWait = 20 * time.Millisecond
	for range maxVideoStreams {
		if err := s.takeStream(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.takeStream(context.Background()); !errors.Is(err, ErrMediaBusy) {
		t.Fatalf("a third conversion: %v", err)
	}
	go func() { time.Sleep(5 * time.Millisecond); <-s.streamSlots }()
	if err := s.takeStream(context.Background()); err != nil {
		t.Fatalf("a seek replacing a stream did not get its slot: %v", err)
	}
}
