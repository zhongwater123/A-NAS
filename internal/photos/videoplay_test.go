package photos

import (
	"encoding/binary"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/mediaplay"
)

// How a video plays is decided as in the media center (internal/mediaplay);
// the library describes its videos from what it read at import.
func TestVideosDescribeThemselvesForPlayback(t *testing.T) {
	phone := videoInfo{Container: "mov,mp4,m4a,3gp,3g2,mj2", Codec: "hevc", PixelFormat: "yuv420p10le", Audio: "aac"}
	source := phone.playSource("video/quicktime")
	if source.ThreeGPP || source.Video == nil || source.Video.Codec != "hevc" || source.Video.PixelFormat != "yuv420p10le" ||
		len(source.Audio) != 1 || source.Audio[0] != (mediaplay.Audio{Codec: "aac", Default: true}) {
		t.Fatalf("playSource() = %+v", source)
	}
	if !phone.playSource("video/3gpp").ThreeGPP {
		t.Fatal("a 3GPP original is not marked")
	}
	if silent := (videoInfo{Container: "mpegts", Codec: "h264"}).playSource("video/mp2t"); silent.Audio != nil {
		t.Fatalf("a silent video plays with %+v", silent.Audio)
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
