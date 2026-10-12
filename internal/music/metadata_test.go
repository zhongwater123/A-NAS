package music

import (
	"reflect"
	"testing"

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
