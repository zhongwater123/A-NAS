package photos

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/zhongwater123/A-NAS/internal/mediaworker"
)

// Videos are photo assets too (docs/specs/photo-video.md): they share the
// Catalog, Policy, albums, copies and trash with photos, and their original
// is an immutable content object like any other. What sets them apart is
// kept per content object in the videos table: the Media Worker reads a
// video once at import, a frame of it becomes the thumbnail, and a browser
// plays either the original or a stream the Worker converts it into.
// Videos take no part in local AI: no vector, detection or face scan is
// ever queued for them, search matches them by name and user tags only, and
// a member's assistant does not see them.

var (
	// ErrNotVideo means the asset is a photo where a video was asked for.
	ErrNotVideo = errors.New("this photo asset is not a video")
	// ErrInvalidStart means a stream was asked to start outside the video.
	ErrInvalidStart = errors.New("the stream would start outside the video")
	// ErrMediaBusy means the device is converting as many videos as it
	// plays at once.
	ErrMediaBusy = errors.New("the device is converting as many videos as it can")
)

const (
	// maxVideoStreams bounds the videos converting for playback at once.
	// Playing an original directly takes no slot.
	maxVideoStreams = 2
	// videoStreamWait is how long a conversion waits for a slot: a seek
	// replaces the viewer's stream with one starting elsewhere, and its
	// request can arrive before the stream it replaces has let go.
	videoStreamWait   = 3 * time.Second
	videoProbeTimeout = 15 * time.Second
	// posterEdge is the long edge of the frame the viewer shows until the
	// video plays.
	posterEdge    = 1600
	posterQuality = 88
	frameTimeout  = 30 * time.Second
	// transcodeHeight and transcodeBitrate bound a video converted for a
	// browser that cannot decode it.
	transcodeHeight  = 1080
	transcodeBitrate = 8000
)

// videoInfo is what deciding how a browser plays a video needs.
type videoInfo struct {
	Container   string `json:"container"`
	Codec       string `json:"codec"`
	PixelFormat string `json:"pixelFormat,omitempty"`
	// Audio is the first audio stream's codec, empty without sound.
	Audio string `json:"audio,omitempty"`
}

// videoFacts is what the Media Worker read from a video original.
type videoFacts struct {
	duration float64
	info     videoInfo
	// liveID is the Apple Live Photo identifier the video carries, and clip
	// whether it is short and complete enough to be a Live Photo's video.
	liveID string
	clip   bool
}

// notVideo is the SQL condition that the content object objectID names is
// not a video original; only such objects take part in local AI.
func notVideo(objectID string) string {
	return "NOT EXISTS (SELECT 1 FROM videos vo WHERE vo.object_id = " + objectID + ")"
}

// hiddenVideos is the SQL condition, with its leading AND, that keeps
// videos out of what a member's assistant reads: they take no part in AI.
func hiddenVideos(p Principal, objectID string) string {
	if !p.Assistant {
		return ""
	}
	return " AND " + notVideo(objectID)
}

// readVideo asks the Media Worker what a staged video original holds and
// accepts it only with a readable video stream of a known container.
func (s *Service) readVideo(ctx context.Context, staged *stagedObject) error {
	if s.media == nil {
		return mediaworker.ErrUnavailable
	}
	file, err := s.root.Open(staged.name)
	if err != nil {
		return err
	}
	defer file.Close()
	probe, err := s.media.Probe(mediaworker.WithTimeout(ctx, videoProbeTimeout), file)
	if errors.Is(err, mediaworker.ErrUnreadable) {
		return ErrUnsupportedType
	}
	if err != nil {
		return err
	}
	facts, metadata, ok := videoFromProbe(probe)
	if !ok {
		return ErrUnsupportedType
	}
	staged.metadata, staged.video = metadata, &facts
	return nil
}

// videoContainers are the FFmpeg formats a video original may come in.
var videoContainers = []string{"mov", "mp4", "matroska", "webm", "avi", "mpegts"}

func videoFromProbe(probe mediaworker.Probe) (videoFacts, photoMetadata, bool) {
	duration := probe.Format.Duration
	if math.IsNaN(duration) || math.IsInf(duration, 0) || duration <= 0 {
		return videoFacts{}, photoMetadata{}, false
	}
	if !slices.ContainsFunc(strings.Split(probe.Format.Name, ","), func(name string) bool { return slices.Contains(videoContainers, name) }) {
		return videoFacts{}, photoMetadata{}, false
	}
	facts := videoFacts{duration: duration, info: videoInfo{Container: probe.Format.Name},
		liveID: probe.Format.Tags[appleIdentifierTag], clip: validMotionProbe(probe)}
	var metadata photoMetadata
	found := false
	for _, stream := range probe.Streams {
		switch {
		case stream.Type == "video" && !stream.AttachedPic && !found:
			found = true
			if stream.Width <= 0 || stream.Height <= 0 || int64(stream.Width) > maxImagePixels/int64(stream.Height) {
				return videoFacts{}, photoMetadata{}, false
			}
			facts.info.Codec, facts.info.PixelFormat = stream.Codec, stream.PixelFormat
			// Players turn the picture by the display matrix; the timeline
			// lays it out as it is seen.
			metadata = photoMetadata{width: stream.Width, height: stream.Height, orientation: 1}
			if turn := ((stream.Rotation % 360) + 360) % 360; turn == 90 || turn == 270 {
				metadata.width, metadata.height = stream.Height, stream.Width
			}
		case stream.Type == "audio" && facts.info.Audio == "":
			facts.info.Audio = stream.Codec
		}
	}
	if !found {
		return videoFacts{}, photoMetadata{}, false
	}
	metadata.takenAt = videoTakenAt(probe.Format.Tags)
	return facts, metadata, true
}

// videoTakenAt reads when a video was recorded: Apple's creation date keeps
// the local time with its offset; the container's creation time is UTC.
// Cameras that never set it leave 1904 or 1970.
func videoTakenAt(tags map[string]string) time.Time {
	for _, candidate := range []struct {
		tag     string
		layouts []string
	}{
		{"com.apple.quicktime.creationdate", []string{"2006-01-02T15:04:05-0700", time.RFC3339Nano}},
		{"creation_time", []string{time.RFC3339Nano, time.DateTime}},
	} {
		value := strings.TrimSpace(tags[candidate.tag])
		for _, layout := range candidate.layouts {
			if at, err := time.Parse(layout, value); err == nil && at.Year() > 1970 {
				return at
			}
		}
	}
	return time.Time{}
}

// insertVideo records what was read from a video original; the facts
// belong to its bytes, so a copy or duplicate reuses them.
func (s *Service) insertVideo(ctx context.Context, tx *sql.Tx, objectID string, facts *videoFacts) error {
	info, err := json.Marshal(facts.info)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO videos(object_id, duration, info) VALUES(?, ?, ?) ON CONFLICT(object_id) DO NOTHING",
		objectID, facts.duration, string(info))
	return err
}

func (s *Service) videoInfo(ctx context.Context, objectID string) (videoInfo, error) {
	var encoded string
	if err := s.db.QueryRowContext(ctx, "SELECT info FROM videos WHERE object_id = ?", objectID).Scan(&encoded); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return videoInfo{}, ErrNotVideo
		}
		return videoInfo{}, err
	}
	var info videoInfo
	if err := json.Unmarshal([]byte(encoded), &info); err != nil {
		return videoInfo{}, err
	}
	return info, nil
}

// posterTime is where a video's thumbnail and poster come from: a moment
// in, past the black frames many recordings start with.
func posterTime(duration float64) float64 { return math.Min(duration/10, 1) }

// renderVideoFrame renders a frame of a video as an upright JPEG; FFmpeg
// already turns it by the display matrix.
func (s *Service) renderVideoFrame(ctx context.Context, file *os.File, duration float64, edge, quality int) ([]byte, int, int, error) {
	if s.media == nil {
		return nil, 0, 0, mediaworker.ErrUnavailable
	}
	ctx = mediaworker.WithTimeout(ctx, frameTimeout)
	at := posterTime(duration)
	frame, err := s.media.Frame(ctx, file, at, posterEdge)
	if errors.Is(err, mediaworker.ErrUnreadable) && at > 0 {
		// A seek into a stream without an index, such as a transport
		// stream with few keyframes, can find nothing to decode.
		frame, err = s.media.Frame(ctx, file, 0, posterEdge)
	}
	if err != nil {
		return nil, 0, 0, err
	}
	return renderUpright(bytes.NewReader(frame), 1, edge, quality)
}

// poster is the frame a viewer shows until the video plays. It is rendered
// on demand, as HEIC previews are, and never stored.
func (s *Service) poster(ctx context.Context, record assetRecord, original *os.File) (Content, error) {
	data, _, _, err := s.renderVideoFrame(ctx, original, record.Video.Duration, posterEdge, posterQuality)
	if err != nil {
		return Content{}, err
	}
	file, err := memoryFile(data)
	if err != nil {
		return Content{}, err
	}
	return Content{Name: record.Name + ".jpg", MediaType: "image/jpeg", SizeBytes: int64(len(data)), ETag: `"` + record.objectID + `-poster-v1"`, Reader: file}, nil
}

// BrowserCaps says what a browser decodes beyond H.264 with AAC, MP3, Opus,
// Vorbis or FLAC sound in MP4.
type BrowserCaps struct {
	HEVC, AV1, VP9, WebM, Matroska, AC3, EAC3 bool
}

// Video playback modes: the original itself, the original's video in a new
// container, or a full conversion.
const (
	PlayDirect    = "direct"
	PlayRemux     = "remux"
	PlayTranscode = "transcode"
)

// VideoPlayback is how a browser plays a video.
type VideoPlayback struct {
	Mode     string  `json:"mode"`
	Duration float64 `json:"duration"`
	// Mime is the type of a converted stream, for Media Source Extensions.
	Mime string `json:"mime,omitempty"`
}

var browserAudio = []string{"aac", "mp3", "opus", "vorbis", "flac"}

// decideVideo picks the cheapest way a browser with caps plays a video, as
// the media center does: the original, its video copied into fragmented MP4
// with the sound converted, or a conversion to H.264 of at most 1080 lines.
func decideVideo(info videoInfo, caps BrowserCaps) (string, mediaworker.StreamRequest) {
	tenBit := strings.Contains(info.PixelFormat, "10")
	videoOK := false
	switch info.Codec {
	case "h264":
		// Chrome does not decode 10-bit H.264.
		videoOK = !tenBit
	case "hevc":
		videoOK = caps.HEVC
	case "av1":
		videoOK = caps.AV1
	case "vp9":
		videoOK = caps.VP9 || caps.WebM
	case "vp8":
		videoOK = caps.WebM
	}
	audioOK := info.Audio == "" || slices.Contains(browserAudio, info.Audio) || info.Audio == "ac3" && caps.AC3 || info.Audio == "eac3" && caps.EAC3
	containerOK := false
	formats := strings.Split(info.Container, ",")
	switch {
	case slices.Contains(formats, "mov") || slices.Contains(formats, "mp4"):
		containerOK = true
	case slices.Contains(formats, "matroska"):
		webm := (info.Codec == "vp8" || info.Codec == "vp9" || info.Codec == "av1") && (info.Audio == "" || info.Audio == "opus" || info.Audio == "vorbis")
		containerOK = webm && caps.WebM || caps.Matroska
	}
	request := mediaworker.StreamRequest{AudioTrack: -1}
	if info.Audio != "" {
		request.AudioTrack = 0
	}
	if videoOK && audioOK && containerOK {
		return PlayDirect, request
	}
	request.CopyAudio = info.Audio == "aac" || info.Audio == "mp3"
	if info.Codec == "h264" && !tenBit || info.Codec == "hevc" && caps.HEVC {
		request.CopyVideo, request.HEVC = true, info.Codec == "hevc"
		return PlayRemux, request
	}
	request.Height, request.MaxBitrate = transcodeHeight, transcodeBitrate
	return PlayTranscode, request
}

// streamMime is the type of the fragmented MP4 a stream produces. Browsers
// take each codec's profile and level from the stream itself.
func streamMime(info videoInfo, request mediaworker.StreamRequest) string {
	codecs := []string{"avc1.640033"}
	if request.CopyVideo && request.HEVC {
		codecs[0] = "hvc1.1.6.L153.B0"
	}
	switch {
	case request.AudioTrack < 0:
	case request.CopyAudio && info.Audio == "mp3":
		codecs = append(codecs, "mp4a.69")
	default:
		codecs = append(codecs, "mp4a.40.2")
	}
	return `video/mp4; codecs="` + strings.Join(codecs, ",") + `"`
}

// playableVideo returns a video asset p may play. An assistant plays none.
func (s *Service) playableVideo(ctx context.Context, p Principal, assetID string) (assetRecord, error) {
	if p.Assistant {
		return assetRecord{}, ErrForbidden
	}
	record, err := s.visibleAsset(ctx, s.db, p, assetID)
	if err != nil {
		return assetRecord{}, err
	}
	if record.Video == nil {
		return assetRecord{}, ErrNotVideo
	}
	return record, nil
}

// VideoPlayback decides how a browser with caps plays a video asset.
func (s *Service) VideoPlayback(ctx context.Context, p Principal, assetID string, caps BrowserCaps) (VideoPlayback, error) {
	record, err := s.playableVideo(ctx, p, assetID)
	if err != nil {
		return VideoPlayback{}, err
	}
	info, err := s.videoInfo(ctx, record.objectID)
	if err != nil {
		return VideoPlayback{}, err
	}
	mode, request := decideVideo(info, caps)
	playback := VideoPlayback{Mode: mode, Duration: record.Video.Duration}
	if mode != PlayDirect {
		playback.Mime = streamMime(info, request)
	}
	return playback, nil
}

// OpenVideo opens a video's original for a browser to play directly, typed
// the way browsers accept it: they play QuickTime files with codecs they
// know as MP4.
func (s *Service) OpenVideo(ctx context.Context, p Principal, assetID string) (Content, error) {
	record, err := s.playableVideo(ctx, p, assetID)
	if err != nil {
		return Content{}, err
	}
	content, err := s.Open(ctx, p, record.ID)
	if err != nil {
		return Content{}, err
	}
	switch content.MediaType {
	case "video/quicktime", "video/3gpp":
		content.MediaType = "video/mp4"
	}
	return content, nil
}

// VideoStreamRequest asks for a video converted from Start seconds on.
// Transcode converts it fully even when a browser with Caps could take the
// original or its video as it is, for when that failed.
type VideoStreamRequest struct {
	Start     float64
	Transcode bool
	Caps      BrowserCaps
}

// StreamVideo converts a video into fragmented MP4 through the Media Worker.
// The request owns the conversion: closing the stream ends FFmpeg and frees
// its slot. At most maxVideoStreams run at once.
func (s *Service) StreamVideo(ctx context.Context, p Principal, assetID string, request VideoStreamRequest) (io.ReadCloser, error) {
	record, err := s.playableVideo(ctx, p, assetID)
	if err != nil {
		return nil, err
	}
	if math.IsNaN(request.Start) || request.Start < 0 || request.Start >= record.Video.Duration {
		return nil, ErrInvalidStart
	}
	if s.media == nil {
		return nil, mediaworker.ErrUnavailable
	}
	info, err := s.videoInfo(ctx, record.objectID)
	if err != nil {
		return nil, err
	}
	mode, options := decideVideo(info, request.Caps)
	if request.Transcode || mode == PlayDirect {
		options.CopyVideo, options.HEVC, options.CopyAudio = false, false, false
		options.Height, options.MaxBitrate = transcodeHeight, transcodeBitrate
	}
	options.Start = request.Start
	if err := s.takeStream(ctx); err != nil {
		return nil, err
	}
	release := func() { <-s.streamSlots }
	original, err := s.openObject(record.objectID)
	if err != nil {
		release()
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	body, err := s.media.Stream(ctx, original, options)
	_ = original.Close()
	if err != nil {
		cancel()
		release()
		return nil, err
	}
	return motionStream{body, cancel, release}, nil
}

func (s *Service) takeStream(ctx context.Context) error {
	select {
	case s.streamSlots <- struct{}{}:
		return nil
	default:
	}
	timer := time.NewTimer(s.streamWait)
	defer timer.Stop()
	select {
	case s.streamSlots <- struct{}{}:
		return nil
	case <-timer.C:
		return ErrMediaBusy
	case <-ctx.Done():
		return ctx.Err()
	}
}
