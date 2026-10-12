package media

import (
	"bytes"
	"context"
	"io"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/files"
)

const (
	ModeDirect      = "direct"
	ModeRemux       = "remux"
	ModeTranscode   = "transcode"
	QualityOriginal = "original"
)

type quality struct {
	name    string
	height  int
	bitrate int
}

// qualities are the transcoding choices offered under the original.
var qualities = []quality{{"1080p", 1080, 8000}, {"720p", 720, 4000}, {"480p", 480, 1500}}

func qualityByName(name string) (quality, bool) {
	for _, candidate := range qualities {
		if candidate.name == name {
			return candidate, true
		}
	}
	return quality{}, false
}

// decision is how one video plays.
type decision struct {
	mode    string
	reason  string
	options StreamOptions
}

var browserAudio = map[string]bool{"aac": true, "mp3": true, "opus": true, "vorbis": true, "flac": true}

func audioPlayable(codec string, caps Capabilities) bool {
	return browserAudio[codec] || codec == "ac3" && caps.AC3 || codec == "eac3" && caps.EAC3
}

// videoPlayable reports whether the browser decodes the video stream. Chrome
// does not decode 10-bit H.264.
func videoPlayable(video *VideoStream, caps Capabilities) bool {
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

// decide picks the cheapest way to play: the original file, the video
// copied with the audio converted, or a full transcode.
func decide(info MediaInfo, fileName string, caps Capabilities, audio int, chosen string) decision {
	options := StreamOptions{AudioTrack: -1}
	if len(info.Audio) > 0 {
		options.AudioTrack = defaultAudio(info.Audio)
		if audio >= 0 && audio < len(info.Audio) {
			options.AudioTrack = audio
		}
	}
	target, lower := qualityByName(chosen)
	if lower && info.Video != nil && info.Video.Height > 0 && info.Video.Height <= target.height {
		lower = false
	}
	var selected *AudioStream
	if options.AudioTrack >= 0 {
		selected = &info.Audio[options.AudioTrack]
	}
	videoOK := videoPlayable(info.Video, caps)
	audioOK := selected == nil || audioPlayable(selected.Codec, caps)
	if !lower && videoOK && audioOK && options.AudioTrack == defaultAudio(info.Audio) && containerPlayable(info, fileName, caps) {
		return decision{mode: ModeDirect, options: options}
	}
	options.CopyAudio = selected != nil && (selected.Codec == "aac" || selected.Codec == "mp3")
	// Fragmented MP4 carries H.264 and, where the browser decodes it, HEVC.
	copyable := info.Video != nil && (info.Video.Codec == "h264" && videoOK || info.Video.Codec == "hevc" && caps.HEVC)
	if !lower && copyable {
		options.CopyVideo, options.HEVC = true, info.Video.Codec == "hevc"
		reason := "浏览器不支持此封装格式，正在转换封装"
		if !audioOK {
			reason = "浏览器无法播放 " + codecLabel(selected.Codec) + " 音频，正在转换音频"
		} else if options.AudioTrack != defaultAudio(info.Audio) {
			reason = "切换音轨需要转换封装"
		}
		return decision{mode: ModeRemux, reason: reason, options: options}
	}
	reason := "浏览器无法解码此视频，正在实时转码"
	if lower {
		options.Height, options.MaxBitrate = target.height, target.bitrate
		reason = "已切换到 " + target.name
	} else if info.Video != nil && info.Video.Height > 2160 {
		options.Height = 2160
	}
	return decision{mode: ModeTranscode, reason: reason, options: options}
}

func defaultAudio(streams []AudioStream) int {
	if len(streams) == 0 {
		return -1
	}
	for position, stream := range streams {
		if stream.Default {
			return position
		}
	}
	return 0
}

// containerPlayable: browsers open MP4 and WebM; Chromium also opens
// Matroska when the codecs inside are ones it decodes.
func containerPlayable(info MediaInfo, fileName string, caps Capabilities) bool {
	format := info.Container
	extension := strings.ToLower(path.Ext(fileName))
	switch {
	case strings.Contains(format, "mp4") || strings.Contains(format, "mov"):
		return extension != ".3gp" || info.Video == nil || info.Video.Codec == "h264"
	case strings.Contains(format, "matroska"):
		webm := info.Video == nil || info.Video.Codec == "vp8" || info.Video.Codec == "vp9" || info.Video.Codec == "av1"
		for _, audio := range info.Audio {
			webm = webm && (audio.Codec == "opus" || audio.Codec == "vorbis")
		}
		if webm {
			return caps.WebM
		}
		return caps.Matroska
	}
	return false
}

func availableQualities(info MediaInfo) []string {
	names := []string{QualityOriginal}
	for _, candidate := range qualities {
		if info.Video == nil || info.Video.Height == 0 || info.Video.Height > candidate.height {
			names = append(names, candidate.name)
		}
	}
	return names
}

// Playback decides how the browser should play a video. The URL is left to
// the HTTP layer.
func (s *Service) Playback(ctx context.Context, actor accounts.User, videoID string, caps Capabilities, audio int, chosen string) (Playback, error) {
	video, _, err := s.visibleVideo(ctx, actor, videoID)
	if err != nil {
		return Playback{}, err
	}
	if chosen == "" {
		chosen = QualityOriginal
	}
	if _, ok := qualityByName(chosen); !ok && chosen != QualityOriginal {
		return Playback{}, ErrInvalid
	}
	info, err := s.mediaInfo(ctx, videoID)
	if err != nil {
		return Playback{}, err
	}
	playback := Playback{Mode: ModeDirect, Duration: video.Duration, Audio: audio, Quality: chosen, Qualities: availableQualities(info)}
	if s.processor == nil || video.ProbeState != "ready" {
		// Without what FFmpeg reads, only the original can be tried.
		playback.Quality, playback.Qualities = QualityOriginal, []string{QualityOriginal}
		return playback, nil
	}
	choice := decide(info, video.FileName, caps, audio, chosen)
	playback.Mode, playback.Reason, playback.Audio = choice.mode, choice.reason, choice.options.AudioTrack
	return playback, nil
}

// Original opens the video file itself for direct play, as the actor.
func (s *Service) Original(ctx context.Context, actor accounts.User, videoID string) (files.Content, string, error) {
	video, _, err := s.visibleVideo(ctx, actor, videoID)
	if err != nil {
		return files.Content{}, "", err
	}
	content, err := s.files.OpenContent(ctx, actor, video.EntryID)
	if err != nil {
		return files.Content{}, "", err
	}
	return content, contentType(video.FileName), nil
}

func contentType(name string) string {
	switch strings.ToLower(path.Ext(name)) {
	case ".webm":
		return "video/webm"
	case ".mkv":
		return "video/x-matroska"
	case ".ogv":
		return "video/ogg"
	}
	// Chromium plays QuickTime files with codecs it knows as MP4.
	return "video/mp4"
}

// StreamRequest asks for a converted stream starting at Start seconds.
type StreamRequest struct {
	Mode    string
	Start   float64
	Audio   int
	Quality string
	Caps    Capabilities
}

// Stream converts a video with FFmpeg into fragmented MP4. At most
// MaxStreams run at once; closing the stream ends FFmpeg.
func (s *Service) Stream(ctx context.Context, actor accounts.User, videoID string, request StreamRequest) (io.ReadCloser, error) {
	if s.processor == nil {
		return nil, ErrUnavailable
	}
	video, _, err := s.visibleVideo(ctx, actor, videoID)
	if err != nil {
		return nil, err
	}
	if request.Start < 0 || video.Duration > 0 && request.Start > video.Duration {
		return nil, ErrInvalid
	}
	info, err := s.mediaInfo(ctx, videoID)
	if err != nil {
		return nil, err
	}
	if request.Quality == "" {
		request.Quality = QualityOriginal
	}
	choice := decide(info, video.FileName, request.Caps, request.Audio, request.Quality)
	options := choice.options
	if request.Mode == ModeTranscode && options.CopyVideo {
		options.CopyVideo, options.HEVC = false, false
	}
	options.Start = request.Start
	select {
	case s.streams <- struct{}{}:
	default:
		return nil, ErrBusy
	}
	release := sync.OnceFunc(func() { <-s.streams })
	content, err := s.files.OpenContent(ctx, actor, video.EntryID)
	if err != nil {
		release()
		return nil, err
	}
	stream, err := s.processor.Stream(ctx, content.Reader, options)
	_ = content.Reader.Close()
	if err != nil {
		release()
		return nil, err
	}
	return &releasingStream{ReadCloser: stream, release: release}, nil
}

type releasingStream struct {
	io.ReadCloser
	release func()
}

func (r *releasingStream) Close() error {
	err := r.ReadCloser.Close()
	r.release()
	return err
}

// Subtitle returns a subtitle track as WebVTT, converted once per file
// version and kept in the cache.
func (s *Service) Subtitle(ctx context.Context, actor accounts.User, videoID, trackID string) ([]byte, error) {
	video, _, err := s.visibleVideo(ctx, actor, videoID)
	if err != nil {
		return nil, err
	}
	info, err := s.mediaInfo(ctx, videoID)
	if err != nil {
		return nil, err
	}
	var track *SubtitleTrack
	for _, candidate := range subtitleTracks(video, info) {
		if candidate.ID == trackID {
			track = &candidate
			break
		}
	}
	if track == nil {
		return nil, ErrNotFound
	}
	cacheKey := ""
	if s.cacheDir != "" {
		cacheKey = s.subtitlePath(video, trackID)
		if cached, err := os.ReadFile(cacheKey); err == nil {
			return cached, nil
		}
	}
	var data []byte
	if track.External {
		data, err = s.externalSubtitle(ctx, actor, video, trackID)
	} else {
		data, err = s.embeddedSubtitle(ctx, actor, video, trackID)
	}
	if err != nil {
		return nil, err
	}
	if cacheKey != "" {
		_ = writeAtomically(cacheKey, data)
	}
	return data, nil
}

func (s *Service) subtitlePath(video *videoRow, trackID string) string {
	return filepath.Join(s.cacheDir, "subtitles", cacheName(video.ID)+"-"+trackID+"-"+video.Modified.UTC().Format("20060102150405")+".vtt")
}

func (s *Service) externalSubtitle(ctx context.Context, actor accounts.User, video *videoRow, trackID string) ([]byte, error) {
	index, err := strconv.Atoi(strings.TrimPrefix(trackID, "x"))
	if err != nil || index < 0 || index >= len(video.Subtitles) {
		return nil, ErrNotFound
	}
	subtitle := video.Subtitles[index]
	content, err := s.files.OpenContent(ctx, actor, subtitle.EntryID)
	if err != nil {
		return nil, err
	}
	defer content.Reader.Close()
	if content.SizeBytes > maxSubtitle {
		return nil, ErrInvalid
	}
	head := make([]byte, 64<<10)
	read, _ := io.ReadFull(content.Reader, head)
	if _, err := content.Reader.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	charset := detectCharset(head[:read])
	if s.processor == nil {
		if subtitle.Format == "webvtt" && charset == "" {
			return io.ReadAll(io.LimitReader(content.Reader, maxSubtitle))
		}
		return nil, ErrUnavailable
	}
	jobCtx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	return s.processor.Subtitle(jobCtx, content.Reader, SubtitleSource{Format: subtitle.Format, Charset: charset, Stream: -1})
}

func (s *Service) embeddedSubtitle(ctx context.Context, actor accounts.User, video *videoRow, trackID string) ([]byte, error) {
	if s.processor == nil {
		return nil, ErrUnavailable
	}
	stream, err := strconv.Atoi(strings.TrimPrefix(trackID, "s"))
	if err != nil || stream < 0 {
		return nil, ErrNotFound
	}
	content, err := s.files.OpenContent(ctx, actor, video.EntryID)
	if err != nil {
		return nil, err
	}
	defer content.Reader.Close()
	// Extracting a track reads the whole file.
	jobCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	return s.processor.Subtitle(jobCtx, content.Reader, SubtitleSource{Stream: stream})
}

// maxSubtitle bounds external subtitle files.
const maxSubtitle = 16 << 20

// detectCharset names the encoding FFmpeg should read a subtitle file in:
// UTF-8 needs none, and Chinese subtitles that are not UTF-8 are almost
// always GB18030 (a superset of GBK and GB2312).
func detectCharset(head []byte) string {
	switch {
	case bytes.HasPrefix(head, []byte{0xFF, 0xFE}), bytes.HasPrefix(head, []byte{0xFE, 0xFF}):
		return "UTF-16"
	case utf8.Valid(trimIncompleteRune(head)):
		return ""
	}
	return "GB18030"
}

// trimIncompleteRune drops a multi-byte character cut off at the end of a
// partial read.
func trimIncompleteRune(data []byte) []byte {
	for i := 0; i < utf8.UTFMax && i < len(data); i++ {
		if utf8.Valid(data[:len(data)-i]) {
			return data[:len(data)-i]
		}
	}
	return data
}
