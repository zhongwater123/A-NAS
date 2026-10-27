// Package mediaplay decides how a browser plays a video, for the media
// center and the photo library alike: what the browser says it decodes,
// whether it plays the original, the original's video in fragmented MP4 or
// a conversion, the type of a converted stream and how many conversions run
// at once (docs/architecture/media-center.md#播放). Each service describes its
// videos as a Source and turns a Decision into its own API; the Media Worker
// runs the stream.
package mediaplay

import (
	"math"
	"slices"
	"strings"

	"github.com/zhongwater123/A-NAS/internal/mediaworker"
)

// The ways a video plays: the original itself, the original's video copied
// into fragmented MP4 with its sound converted where needed, or a full
// conversion to H.264.
const (
	Direct    = "direct"
	Remux     = "remux"
	Transcode = "transcode"
)

// Caps says what a browser decodes beyond H.264 with AAC, MP3, Opus, Vorbis
// or FLAC sound in MP4.
type Caps struct {
	HEVC, AV1, VP9, WebM, Matroska, AC3, EAC3 bool
}

// ParseCaps reads the list the players send as their caps parameter: names
// separated by commas. Names it does not know are ignored.
func ParseCaps(list string) Caps {
	var caps Caps
	for name := range strings.SplitSeq(list, ",") {
		switch strings.TrimSpace(name) {
		case "hevc":
			caps.HEVC = true
		case "av1":
			caps.AV1 = true
		case "vp9":
			caps.VP9 = true
		case "webm":
			caps.WebM = true
		case "mkv":
			caps.Matroska = true
		case "ac3":
			caps.AC3 = true
		case "eac3":
			caps.EAC3 = true
		}
	}
	return caps
}

// Source is what deciding needs to know about a video, as FFmpeg read it.
type Source struct {
	// Container is FFmpeg's format name, such as "mov,mp4,m4a,3gp,3g2,mj2".
	Container string
	// ThreeGPP marks a 3GPP file, which plays directly only with H.264.
	ThreeGPP bool
	// Video is the picture; nil for sound alone.
	Video *Video
	// Audio lists the audio streams in order.
	Audio []Audio
}

type Video struct {
	Codec       string
	PixelFormat string
	// Height is the picture's, 0 when unknown.
	Height int
}

type Audio struct {
	Codec string
	// Default marks the stream a player picks by itself.
	Default bool
}

// Quality limits the picture of a conversion.
type Quality struct {
	Name   string
	Height int
	// Bitrate caps the converted video, in kbit/s.
	Bitrate int
}

// Qualities are the conversions offered below the original, best first.
var Qualities = []Quality{{"1080p", 1080, 8000}, {"720p", 720, 4000}, {"480p", 480, 1500}}

// QualityNamed returns the quality called name.
func QualityNamed(name string) (Quality, bool) {
	index := slices.IndexFunc(Qualities, func(quality Quality) bool { return quality.Name == name })
	if index < 0 {
		return Quality{}, false
	}
	return Qualities[index], true
}

// DefaultAudio chooses the audio stream a player picks by itself.
const DefaultAudio = -1

// Choice is what the viewer asked for.
type Choice struct {
	// AudioTrack is the position of the audio stream to hear, or
	// DefaultAudio. A position outside the source is the default too.
	AudioTrack int
	// Lower is a quality the viewer lowered the picture to; the zero
	// Quality keeps the original, and so does one at or above the source's
	// height.
	Lower Quality
	// Ceiling limits a conversion the viewer did not lower; the zero
	// Quality keeps the source's size and leaves the bitrate to the
	// encoder.
	Ceiling Quality
	// Transcode converts fully even where the browser could take the
	// original or its video as it is, because playing it failed.
	Transcode bool
}

// Why says what keeps the original from playing directly.
type Why int

const (
	// Playable: nothing; the original plays.
	Playable Why = iota
	// Container: the browser does not open the original's container.
	Container
	// Sound: the browser does not play the chosen audio stream's codec.
	Sound
	// OtherTrack: the viewer chose an audio stream other than the default.
	OtherTrack
	// Picture: the browser does not decode the picture.
	Picture
	// Lowered: the viewer chose a lower quality.
	Lowered
	// Failed: the browser failed to play what it said it decodes.
	Failed
)

// Decision is how a browser plays a video.
type Decision struct {
	Mode string
	Why  Why
	// Stream is what to ask the Media Worker for unless Mode is Direct;
	// the caller sets where it starts.
	Stream mediaworker.StreamRequest
}

var browserAudio = []string{"aac", "mp3", "opus", "vorbis", "flac"}

// Decide picks the cheapest way a browser with caps plays source: the
// original, its video copied with the sound converted where needed, or a
// conversion to H.264 within the ceiling or the quality the viewer chose.
func Decide(source Source, caps Caps, choice Choice) Decision {
	defaultTrack := defaultAudio(source.Audio)
	request := mediaworker.StreamRequest{AudioTrack: defaultTrack}
	if choice.AudioTrack >= 0 && choice.AudioTrack < len(source.Audio) {
		request.AudioTrack = choice.AudioTrack
	}
	sound := ""
	if request.AudioTrack >= 0 {
		sound = source.Audio[request.AudioTrack].Codec
	}
	lower := choice.Lower.Height > 0 && (source.Video == nil || source.Video.Height <= 0 || source.Video.Height > choice.Lower.Height)
	videoOK := videoPlayable(source.Video, caps)
	audioOK := sound == "" || audioPlayable(sound, caps)
	if !choice.Transcode && !lower && videoOK && audioOK && request.AudioTrack == defaultTrack && containerPlayable(source, caps) {
		return Decision{Mode: Direct, Stream: request}
	}
	request.CopyAudio = sound == "aac" || sound == "mp3"
	// Fragmented MP4 carries H.264 and, where the browser decodes it, HEVC.
	copyable := source.Video != nil && (source.Video.Codec == "h264" && videoOK || source.Video.Codec == "hevc" && caps.HEVC)
	if !choice.Transcode && !lower && copyable {
		request.CopyVideo, request.HEVC = true, source.Video.Codec == "hevc"
		why := Container
		switch {
		case !audioOK:
			why = Sound
		case request.AudioTrack != defaultTrack:
			why = OtherTrack
		}
		return Decision{Mode: Remux, Why: why, Stream: request}
	}
	quality, why := choice.Ceiling, Picture
	switch {
	case lower:
		quality, why = choice.Lower, Lowered
	case choice.Transcode:
		why = Failed
	}
	request.Height, request.MaxBitrate = quality.Height, quality.Bitrate
	return Decision{Mode: Transcode, Why: why, Stream: request}
}

// Converting decides the stream a browser asked for. A browser asks for one
// of a video it could play directly only once playing the original failed,
// so that one is converted fully.
func Converting(source Source, caps Caps, choice Choice) Decision {
	decision := Decide(source, caps, choice)
	if decision.Mode == Direct {
		choice.Transcode = true
		decision = Decide(source, caps, choice)
	}
	return decision
}

func defaultAudio(streams []Audio) int {
	if len(streams) == 0 {
		return -1
	}
	return max(0, slices.IndexFunc(streams, func(stream Audio) bool { return stream.Default }))
}

func audioPlayable(codec string, caps Caps) bool {
	return slices.Contains(browserAudio, codec) || codec == "ac3" && caps.AC3 || codec == "eac3" && caps.EAC3
}

// videoPlayable reports whether the browser decodes the picture. Chrome does
// not decode 10-bit H.264.
func videoPlayable(video *Video, caps Caps) bool {
	if video == nil {
		return true
	}
	switch video.Codec {
	case "h264":
		return !strings.Contains(video.PixelFormat, "10")
	case "hevc":
		return caps.HEVC
	case "av1":
		return caps.AV1
	case "vp9":
		return caps.VP9 || caps.WebM
	case "vp8":
		return caps.WebM
	}
	return false
}

// containerPlayable: browsers open MP4 and QuickTime, and WebM; Chromium
// also opens Matroska when the codecs inside are ones it decodes.
func containerPlayable(source Source, caps Caps) bool {
	formats := strings.Split(source.Container, ",")
	switch {
	case slices.Contains(formats, "mov") || slices.Contains(formats, "mp4"):
		return !source.ThreeGPP || source.Video == nil || source.Video.Codec == "h264"
	case slices.Contains(formats, "matroska"):
		webm := source.Video == nil || source.Video.Codec == "vp8" || source.Video.Codec == "vp9" || source.Video.Codec == "av1"
		for _, audio := range source.Audio {
			webm = webm && (audio.Codec == "opus" || audio.Codec == "vorbis")
		}
		return webm && caps.WebM || caps.Matroska
	}
	return false
}

// StreamMime is the type of the fragmented MP4 that request makes of
// source, for Media Source Extensions. Browsers take each codec's profile
// and level from the stream itself, so the codec strings only name the
// codec.
func StreamMime(source Source, request mediaworker.StreamRequest) string {
	var codecs []string
	switch {
	case source.Video == nil:
	case request.CopyVideo && request.HEVC:
		codecs = append(codecs, "hvc1.1.6.L153.B0")
	default:
		codecs = append(codecs, "avc1.640033")
	}
	if request.AudioTrack >= 0 && request.AudioTrack < len(source.Audio) {
		if request.CopyAudio && source.Audio[request.AudioTrack].Codec == "mp3" {
			codecs = append(codecs, "mp4a.69")
		} else {
			codecs = append(codecs, "mp4a.40.2")
		}
	}
	return `video/mp4; codecs="` + strings.Join(codecs, ",") + `"`
}

// StartWithin reports whether a stream may start start seconds into a video
// that lasts duration seconds; a duration of 0 is unknown.
func StartWithin(start, duration float64) bool {
	if math.IsNaN(start) || math.IsInf(start, 0) || start < 0 {
		return false
	}
	return duration <= 0 || start < duration
}
