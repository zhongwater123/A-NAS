package music

import (
	"reflect"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/ffmpegworker"
	"github.com/zhongwater123/A-NAS/internal/imagetest"

	"golang.org/x/text/encoding/simplifiedchinese"
)

func gbk(t *testing.T, value string) []byte {
	t.Helper()
	encoded, err := simplifiedchinese.GBK.NewEncoder().Bytes([]byte(value))
	if err != nil {
		t.Fatalf("encode GBK: %v", err)
	}
	return encoded
}

// asLatin1 is what dhowden/tag returns for bytes in an ISO-8859-1 frame.
func asLatin1(raw []byte) string {
	runes := make([]rune, len(raw))
	for i, b := range raw {
		runes[i] = rune(b)
	}
	return string(runes)
}

func TestRepairTextRecoversGBKButKeepsLatin1Names(t *testing.T) {
	cases := []struct{ name, input, want string }{
		{"GBK in an ISO-8859-1 frame", asLatin1(gbk(t, "周杰伦 - 晴天")), "周杰伦 - 晴天"},
		{"GBK in a Vorbis comment", string(gbk(t, "七里香")), "七里香"},
		{"UTF-8 in an ISO-8859-1 frame", asLatin1([]byte("陈奕迅")), "陈奕迅"},
		{"accented name at the end", "Beyoncé", "Beyoncé"},
		{"accent followed by ASCII", "Motörhead", "Motörhead"},
		{"plain UTF-8", "Taylor Swift", "Taylor Swift"},
		{"trailing NUL padding", "Hello\x00\x00", "Hello"},
	}
	for _, test := range cases {
		if got := repairText(test.input); got != test.want {
			t.Errorf("%s: repairText = %q, want %q", test.name, got, test.want)
		}
	}
}

func TestDecodeTextReadsUTF8AndGB18030Lyrics(t *testing.T) {
	if got := decodeText(append([]byte{0xef, 0xbb, 0xbf}, "[00:01.00]你好"...)); got != "[00:01.00]你好" {
		t.Errorf("UTF-8 with BOM = %q", got)
	}
	if got := decodeText(gbk(t, "[00:01.00]你好")); got != "[00:01.00]你好" {
		t.Errorf("GBK = %q", got)
	}
}

func TestSplitArtistsAndGenres(t *testing.T) {
	cases := map[string][]string{
		"周杰伦/费玉清":                  {"周杰伦", "费玉清"},
		"陈奕迅、王菲":                   {"陈奕迅", "王菲"},
		"Daft Punk feat. Pharrell": {"Daft Punk", "Pharrell"},
		"AC/DC":                    {"AC/DC"},
		"Simon & Garfunkel":        {"Simon & Garfunkel"},
		"A; a; B":                  {"A", "B"},
		"":                         nil,
	}
	for input, want := range cases {
		if got := splitArtists(input); !reflect.DeepEqual(got, want) {
			t.Errorf("splitArtists(%q) = %q, want %q", input, got, want)
		}
	}
	if got := splitGenres("(17)Rock; Pop/流行"); !reflect.DeepEqual(got, []string{"Rock", "Pop", "流行"}) {
		t.Errorf("splitGenres = %q", got)
	}
}

func TestFileNamesFillMissingTitleAndArtist(t *testing.T) {
	value := metadata{}
	fillFromName(&value, "周杰伦 - 晴天.mp3")
	if value.Title != "晴天" || value.Artist != "周杰伦" {
		t.Errorf("from name = %+v", value)
	}
	tagged := metadata{Artist: "Jay Chou"}
	fillFromName(&tagged, "周杰伦 - 晴天.mp3")
	if tagged.Title != "晴天" || tagged.Artist != "Jay Chou" {
		t.Errorf("a tagged artist was replaced: %+v", tagged)
	}
}

func TestAlbumGroupingFollowsAlbumArtistOrFolder(t *testing.T) {
	disc1 := row{SpaceID: "s", Dir: "Music/Compilation/CD1", Album: "Hits"}
	disc2 := row{SpaceID: "s", Dir: "Music/Compilation/CD2", Album: "Hits"}
	elsewhere := row{SpaceID: "s", Dir: "Other", Album: "Hits"}
	if albumID(disc1) != albumID(disc2) {
		t.Error("discs of one album were split")
	}
	if albumID(disc1) == albumID(elsewhere) {
		t.Error("albums of the same name in different folders were merged")
	}
	byArtist := row{SpaceID: "s", Dir: "A", Album: "Hits", AlbumArtist: "Various"}
	moved := row{SpaceID: "t", Dir: "B", Album: "hits", AlbumArtist: "various"}
	if albumID(byArtist) != albumID(moved) {
		t.Error("an album artist did not group across folders")
	}
	if albumID(row{Dir: "X"}) != "album:unknown" {
		t.Error("tracks without an album are not grouped as unknown")
	}
}

func TestProbeTagsFillWhatTheFileReaderMissed(t *testing.T) {
	value := metadata{Title: "From the file"}
	format := fromProbe(&value, ffmpegworker.Probe{
		Format:  ffmpegworker.ProbeFormat{Duration: 61.25, Tags: map[string]string{"title": "From the probe", "ALBUM_ARTIST": "群星", "disc": "2/2", "DATE": "1999-04-01"}},
		Streams: []ffmpegworker.ProbeStream{{Type: "audio", Codec: "dsd_lsbf_planar", SampleRate: 352800, SampleFormat: "fltp", Tags: map[string]string{"ARTIST": asLatin1(gbk(t, "王菲"))}}},
	})
	if value.Title != "From the file" || value.AlbumArtist != "群星" || value.Artist != "王菲" || value.DiscNo != 2 || value.Year != 1999 || value.Codec != "dsd_lsbf_planar" {
		t.Errorf("metadata = %+v", value)
	}
	if format.Duration != 61250*time.Millisecond || format.SampleRate != 352800 || format.Bits != 24 || format.Cover {
		t.Errorf("format = %+v", format)
	}
	lossy := metadata{}
	if format := fromProbe(&lossy, ffmpegworker.Probe{Streams: []ffmpegworker.ProbeStream{{Type: "audio", Codec: "wmav2", SampleRate: 44100, SampleFormat: "fltp"}}}); format.Bits != 16 {
		t.Errorf("lossy audio decodes to %d bits", format.Bits)
	}
}

func TestPlaybackRequestsFitWhatBrowsersPlay(t *testing.T) {
	cases := []struct {
		rate, bits int
		want       ffmpegworker.AudioRequest
	}{
		{44100, 16, ffmpegworker.AudioRequest{Start: 5, Bits: 16}},
		{96000, 24, ffmpegworker.AudioRequest{Start: 5, Bits: 24}},
		{352800, 24, ffmpegworker.AudioRequest{Start: 5, SampleRate: 176400, Bits: 24}},
		{384000, 24, ffmpegworker.AudioRequest{Start: 5, SampleRate: 192000, Bits: 24}},
		{0, 0, ffmpegworker.AudioRequest{Start: 5, Bits: 16}},
	}
	for _, test := range cases {
		if got := playbackRequest(test.rate, test.bits, 5); got != test.want {
			t.Errorf("playbackRequest(%d, %d) = %+v, want %+v", test.rate, test.bits, got, test.want)
		}
	}
}

// panickingReader stands for a parser that panics on whatever it reads.
type panickingReader struct{}

func (panickingReader) Read([]byte) (int, error)       { panic("music test: parser bug") }
func (panickingReader) Seek(int64, int) (int64, error) { return 0, nil }

func TestTagsOfAnM4AThatPanicsEveryParserAreEmpty(t *testing.T) {
	panics := imagetest.LoggedPanics(t)
	// The codec is read after the tag guard has recovered; its panic must
	// not escape the scan either.
	if tags := readTags(panickingReader{}, "m4a"); !reflect.DeepEqual(tags, metadata{}) {
		t.Fatalf("readTags() = %+v, want no tags", tags)
	}
	var decoders []string
	for _, logged := range panics() {
		decoders = append(decoders, logged.Decoder)
	}
	if want := []string{"music tags", "music codec"}; !reflect.DeepEqual(decoders, want) {
		t.Fatalf("logged panics of %q, want %q", decoders, want)
	}
}
