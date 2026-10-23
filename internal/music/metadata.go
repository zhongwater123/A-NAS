package music

import (
	"errors"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/abema/go-mp4"
	"github.com/dhowden/tag"
	"github.com/go-audio/wav"
	"github.com/hajimehoshi/go-mp3"
	"github.com/jfreymuth/oggvorbis"
	"github.com/mewkiz/flac"
	"golang.org/x/text/encoding/simplifiedchinese"

	"github.com/zhongwater123/A-NAS/internal/decodepanic"
	"github.com/zhongwater123/A-NAS/internal/mediaworker"
)

// formats maps the audio file extensions the library indexes to their format.
var formats = map[string]string{
	".mp3": "mp3", ".flac": "flac", ".m4a": "m4a", ".aac": "aac",
	".ogg": "ogg", ".oga": "ogg", ".opus": "opus", ".wav": "wav",
	".ape": "ape", ".wma": "wma", ".wv": "wv", ".aif": "aiff", ".aiff": "aiff", ".dsf": "dsf", ".dff": "dff",
}

// browserFormats play in Chromium as they are. The others, and ALAC inside
// .m4a, are decoded to FLAC by the media worker while they play.
var browserFormats = map[string]bool{"mp3": true, "flac": true, "m4a": true, "aac": true, "ogg": true, "opus": true, "wav": true}

// tagFormats are the formats dhowden/tag reads; the media worker's probe
// reads the tags of the others.
var tagFormats = map[string]bool{"mp3": true, "flac": true, "m4a": true, "ogg": true, "opus": true, "dsf": true}

var contentTypes = map[string]string{
	"mp3": "audio/mpeg", "flac": "audio/flac", "m4a": "audio/mp4", "aac": "audio/aac",
	"ogg": "audio/ogg", "opus": "audio/ogg", "wav": "audio/wav",
}

func formatOf(name string) string { return formats[strings.ToLower(path.Ext(name))] }

func playsInBrowser(format, codec string) bool { return browserFormats[format] && codec != "alac" }

// metadata is what one audio file says about itself.
type metadata struct {
	Title       string
	Artist      string
	Album       string
	AlbumArtist string
	Genre       string
	Year        int
	TrackNo     int
	DiscNo      int
	Codec       string
	Picture     []byte
	Lyrics      bool
}

// readTags reads the tags of an audio file with dhowden/tag. A file it
// cannot parse yields no tags; the caller then uses the media worker's probe
// or the file name.
func readTags(r io.ReadSeeker, format string) (result metadata) {
	defer func() {
		// The tag readers trust lengths stored in the file; a malformed file
		// must not stop the scan.
		if recovered := recover(); recovered != nil {
			decodepanic.Log("music tags", recovered)
			result = metadata{}
		}
		if format == "m4a" {
			result.Codec = mp4Codec(r)
		}
	}()
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return metadata{}
	}
	tags, err := tag.ReadFrom(r)
	if err != nil {
		return metadata{}
	}
	result = metadata{
		Title:       repairText(tags.Title()),
		Artist:      repairText(tags.Artist()),
		Album:       repairText(tags.Album()),
		AlbumArtist: repairText(tags.AlbumArtist()),
		Genre:       repairText(tags.Genre()),
		Year:        tags.Year(),
		Lyrics:      strings.TrimSpace(tags.Lyrics()) != "",
	}
	result.TrackNo, _ = tags.Track()
	result.DiscNo, _ = tags.Disc()
	if picture := tags.Picture(); picture != nil && len(picture.Data) > 0 {
		result.Picture = picture.Data
	}
	if result.Year < 0 || result.Year > 9999 {
		result.Year = 0
	}
	return result
}

// readLyrics returns the lyrics embedded in an audio file's tags.
func readLyrics(r io.ReadSeeker) (lyrics string) {
	defer func() {
		if recovered := recover(); recovered != nil {
			decodepanic.Log("music lyrics", recovered)
			lyrics = ""
		}
	}()
	tags, err := tag.ReadFrom(r)
	if err != nil {
		return ""
	}
	return repairText(tags.Lyrics())
}

// audioFormat is what decoding a track for playback needs to know.
type audioFormat struct {
	Duration   time.Duration
	SampleRate int
	Bits       int
	// Cover reports album art stored as an attached picture stream.
	Cover bool
}

// lossyCodecs decode to 16 bits; more would only add size.
var lossyCodecs = map[string]bool{
	"mp3": true, "aac": true, "vorbis": true, "opus": true, "wmav1": true, "wmav2": true, "wmapro": true, "wmavoice": true, "ac3": true, "eac3": true,
}

// fromProbe fills tags the file's own reader did not provide from the media
// worker's probe, and reports the audio format. Container tag names differ in
// case and spelling ("album_artist", "Album Artist"), so they are compared
// without case or separators.
func fromProbe(m *metadata, probe mediaworker.Probe) audioFormat {
	tags := map[string]string{}
	collect := func(values map[string]string) {
		for key, value := range values {
			key = strings.NewReplacer(" ", "", "_", "", "-", "").Replace(strings.ToLower(key))
			if _, ok := tags[key]; !ok && strings.TrimSpace(value) != "" {
				tags[key] = value
			}
		}
	}
	collect(probe.Format.Tags)
	format := audioFormat{Duration: time.Duration(probe.Format.Duration * float64(time.Second))}
	for _, stream := range probe.Streams {
		switch {
		case stream.Type == "video" && stream.AttachedPic:
			format.Cover = true
		case stream.Type == "audio" && format.SampleRate == 0:
			collect(stream.Tags)
			if m.Codec == "" {
				m.Codec = stream.Codec
			}
			format.SampleRate = stream.SampleRate
			format.Bits = 16
			wide := strings.HasPrefix(stream.SampleFormat, "s32") || strings.HasPrefix(stream.SampleFormat, "flt") || strings.HasPrefix(stream.SampleFormat, "dbl")
			if !lossyCodecs[stream.Codec] && (stream.Bits > 16 || stream.Bits == 0 && wide) {
				format.Bits = 24
			}
		}
	}
	set := func(target *string, keys ...string) {
		for _, key := range keys {
			if *target == "" {
				*target = repairText(tags[key])
			}
		}
	}
	set(&m.Title, "title")
	set(&m.Artist, "artist", "performer")
	set(&m.Album, "album")
	set(&m.AlbumArtist, "albumartist")
	set(&m.Genre, "genre")
	number := func(value string) int {
		value, _, _ = strings.Cut(strings.TrimSpace(value), "/")
		parsed, _ := strconv.Atoi(strings.TrimSpace(value))
		return max(parsed, 0)
	}
	if m.Year == 0 {
		for _, key := range []string{"date", "year", "originaldate"} {
			if value := tags[key]; len(value) >= 4 {
				if year := number(value[:4]); year > 0 {
					m.Year = year
					break
				}
			}
		}
	}
	if m.TrackNo == 0 {
		m.TrackNo = number(tags["track"] + tags["tracknumber"])
	}
	if m.DiscNo == 0 {
		m.DiscNo = number(tags["disc"] + tags["discnumber"])
	}
	return format
}

// playbackRequest asks the media worker for FLAC that browsers play: 24 bits
// for wide lossless sources, and sample rates above 192 kHz (DSD decodes to
// 352.8 kHz) halved within their family.
func playbackRequest(sampleRate, bits int, start float64) mediaworker.AudioRequest {
	request := mediaworker.AudioRequest{Start: start, Bits: 16}
	if bits > 16 {
		request.Bits = 24
	}
	if sampleRate > 192000 {
		request.SampleRate = 192000
		if sampleRate%44100 == 0 {
			request.SampleRate = 176400
		}
	}
	return request
}

// fillFromName uses the file name for a missing title, and "Artist - Title"
// file names, common in downloaded collections, for a missing artist too.
func fillFromName(m *metadata, name string) {
	if m.Title != "" {
		return
	}
	base := strings.TrimSpace(strings.TrimSuffix(name, path.Ext(name)))
	if artist, title, ok := strings.Cut(base, " - "); ok && strings.TrimSpace(title) != "" {
		if m.Artist == "" {
			m.Artist = strings.TrimSpace(artist)
		}
		base = strings.TrimSpace(title)
	}
	m.Title = base
}

// mp4Codec names the audio codec of an MP4 file: AAC plays in browsers, ALAC
// does not. It runs in readTags's deferred function, after that guard has
// recovered, so it has a guard of its own.
func mp4Codec(r io.ReadSeeker) (codec string) {
	defer func() {
		if recovered := recover(); recovered != nil {
			decodepanic.Log("music codec", recovered)
			codec = ""
		}
	}()
	path := mp4.BoxPath{mp4.BoxTypeMoov(), mp4.BoxTypeTrak(), mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeStsd(), mp4.StrToBoxType("alac")}
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return ""
	}
	boxes, err := mp4.ExtractBox(r, nil, path)
	if err != nil {
		return ""
	}
	if len(boxes) > 0 {
		return "alac"
	}
	return "aac"
}

// readDuration measures how long a file plays. MP3 frames are counted, since
// a header estimate is wrong for variable bit rates; that reads the whole
// file, so durations are measured after tags in a second scan phase.
func readDuration(r io.ReadSeeker, format string) (duration time.Duration, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			decodepanic.Log("music duration", recovered)
			duration, err = 0, fmt.Errorf("read duration: %v", recovered)
		}
	}()
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return 0, err
	}
	switch format {
	case "mp3":
		decoder, err := mp3.NewDecoder(r)
		if err != nil {
			return 0, err
		}
		// The decoder always produces 16-bit stereo samples.
		if decoder.Length() <= 0 || decoder.SampleRate() <= 0 {
			return 0, errors.New("mp3 length is unknown")
		}
		return seconds(float64(decoder.Length()) / 4 / float64(decoder.SampleRate())), nil
	case "flac":
		stream, err := flac.New(r)
		if err != nil {
			return 0, err
		}
		if stream.Info == nil || stream.Info.SampleRate == 0 || stream.Info.NSamples == 0 {
			return 0, errors.New("flac length is unknown")
		}
		return seconds(float64(stream.Info.NSamples) / float64(stream.Info.SampleRate)), nil
	case "m4a":
		boxes, err := mp4.ExtractBoxWithPayload(r, nil, mp4.BoxPath{mp4.BoxTypeMoov(), mp4.BoxTypeMvhd()})
		if err != nil {
			return 0, err
		}
		for _, box := range boxes {
			if header, ok := box.Payload.(*mp4.Mvhd); ok && header.Timescale > 0 {
				return seconds(float64(header.GetDuration()) / float64(header.Timescale)), nil
			}
		}
		return 0, errors.New("mp4 has no movie header")
	case "ogg":
		reader, err := oggvorbis.NewReader(r)
		if err != nil {
			return 0, err
		}
		if reader.Length() <= 0 || reader.SampleRate() <= 0 {
			return 0, errors.New("ogg length is unknown")
		}
		return seconds(float64(reader.Length()) / float64(reader.SampleRate())), nil
	case "wav":
		return wav.NewDecoder(r).Duration()
	}
	return 0, fmt.Errorf("duration of %s is not measured", format)
}

func seconds(value float64) time.Duration { return time.Duration(value * float64(time.Second)) }

var gb18030 = simplifiedchinese.GB18030

// repairText fixes tag text that was stored in a legacy Chinese encoding.
// Older Chinese taggers wrote GBK bytes into ID3 frames declared as
// ISO-8859-1 (and into Vorbis comments, which must be UTF-8), so they decode
// as Latin-1 mojibake. Text is re-decoded only when every byte above ASCII
// pairs up as a GB2312 double byte; Latin-1 names such as "Beyoncé" or
// "Motörhead" do not, and are kept.
func repairText(value string) string {
	// Only ASCII space is trimmed before repair: Unicode spaces such as U+0085
	// can be the last byte of a UTF-8 character read as Latin-1.
	value = strings.Trim(value, "\x00 \t\r\n")
	if value == "" {
		return ""
	}
	var raw []byte
	switch {
	case !utf8.ValidString(value):
		raw = []byte(value)
	case latin1Only(value):
		raw = make([]byte, 0, len(value))
		for _, r := range value {
			raw = append(raw, byte(r))
		}
		// Some taggers wrote UTF-8 into ISO-8859-1 frames.
		if utf8.Valid(raw) {
			return strings.TrimSpace(string(raw))
		}
	default:
		return value
	}
	if gbPairs(raw) {
		if decoded, err := gb18030.NewDecoder().Bytes(raw); err == nil && utf8.Valid(decoded) {
			return strings.TrimSpace(string(decoded))
		}
	}
	return strings.TrimSpace(strings.ToValidUTF8(value, ""))
}

// latin1Only reports text that holds characters above ASCII but none beyond
// Latin-1, which is what a GBK byte string decoded as ISO-8859-1 looks like.
func latin1Only(value string) bool {
	high := false
	for _, r := range value {
		if r > 0xff {
			return false
		}
		if r >= 0x80 {
			high = true
		}
	}
	return high
}

func gbPairs(raw []byte) bool {
	pairs := 0
	for i := 0; i < len(raw); i++ {
		if raw[i] < 0x80 {
			continue
		}
		if i+1 >= len(raw) || raw[i] < 0x81 || raw[i+1] < 0xa1 || raw[i+1] == 0xff {
			return false
		}
		pairs++
		i++
	}
	return pairs > 0
}

// decodeText reads a sidecar text file, such as an .lrc, in UTF-8 (with or
// without a byte order mark) or, failing that, GB18030.
func decodeText(raw []byte) string {
	raw = trimBOM(raw)
	if utf8.Valid(raw) {
		return string(raw)
	}
	if decoded, err := gb18030.NewDecoder().Bytes(raw); err == nil {
		return string(decoded)
	}
	return strings.ToValidUTF8(string(raw), "")
}

func trimBOM(raw []byte) []byte {
	if len(raw) >= 3 && raw[0] == 0xef && raw[1] == 0xbb && raw[2] == 0xbf {
		return raw[3:]
	}
	return raw
}

// artistSeparators split a credit naming several artists. Tags rarely use
// one convention: Chinese taggers favour "/" and "、", others ";".
var artistSeparators = []string{";", "；", "、", "/", "／", " feat. ", " Feat. ", " ft. ", " featuring "}

// unsplitArtists are names that contain a separator.
var unsplitArtists = map[string]bool{"ac/dc": true}

// splitArtists returns the distinct artists credited in value.
func splitArtists(value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	if unsplitArtists[strings.ToLower(value)] {
		return []string{value}
	}
	parts := []string{value}
	for _, separator := range artistSeparators {
		var next []string
		for _, part := range parts {
			next = append(next, strings.Split(part, separator)...)
		}
		parts = next
	}
	seen := map[string]bool{}
	var artists []string
	for _, part := range parts {
		part = strings.TrimSpace(part)
		key := strings.ToLower(part)
		if part == "" || seen[key] {
			continue
		}
		seen[key] = true
		artists = append(artists, part)
	}
	return artists
}

// splitGenres separates multi-valued genre tags and drops the numeric ID3v1
// references some taggers leave, such as "(17)".
func splitGenres(value string) []string {
	var genres []string
	seen := map[string]bool{}
	for _, part := range strings.FieldsFunc(value, func(r rune) bool { return r == ';' || r == '/' || r == ',' || r == '、' || r == '；' }) {
		part = strings.TrimSpace(part)
		if inner, ok := strings.CutPrefix(part, "("); ok {
			if number, rest, ok := strings.Cut(inner, ")"); ok {
				if _, err := strconv.Atoi(number); err == nil {
					part = strings.TrimSpace(rest)
				}
			}
		}
		key := strings.ToLower(part)
		if part == "" || seen[key] || strings.IndexFunc(part, unicode.IsLetter) < 0 {
			continue
		}
		seen[key] = true
		genres = append(genres, part)
	}
	return genres
}
