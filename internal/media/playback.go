package media

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/files"
	"github.com/zhongwater123/A-NAS/internal/mediaplay"
)

const (
	ModeDirect      = mediaplay.Direct
	ModeRemux       = mediaplay.Remux
	ModeTranscode   = mediaplay.Transcode
	QualityOriginal = "original"
)

// playSource describes a video for deciding how it plays, which the media
// center does as the photo library does (internal/mediaplay): by what FFmpeg
// read and by the file's name.
func playSource(info MediaInfo, fileName string) mediaplay.Source {
	source := mediaplay.Source{Container: info.Container, ThreeGPP: strings.EqualFold(path.Ext(fileName), ".3gp")}
	if info.Video != nil {
		source.Video = &mediaplay.Video{Codec: info.Video.Codec, PixelFormat: info.Video.PixelFormat, Height: info.Video.Height}
	}
	for _, audio := range info.Audio {
		source.Audio = append(source.Audio, mediaplay.Audio{Codec: audio.Codec, Default: audio.Default})
	}
	return source
}

// playChoice is the viewer's audio stream, by position or -1 for the
// default, and quality, by name.
func playChoice(audio int, quality string) mediaplay.Choice {
	lower, _ := mediaplay.QualityNamed(quality)
	return mediaplay.Choice{AudioTrack: audio, Lower: lower, Ceiling: originalCeiling}
}

// originalCeiling limits converting the original: it keeps its size up to
// 2160 lines, without a bitrate cap.
var originalCeiling = mediaplay.Quality{Name: QualityOriginal, Height: 2160}

// reason tells the viewer why the original does not play directly.
func reason(decision mediaplay.Decision, source mediaplay.Source, choice mediaplay.Choice) string {
	switch decision.Why {
	case mediaplay.Container:
		return "浏览器不支持此封装格式，正在转换封装"
	case mediaplay.Sound:
		return "浏览器无法播放 " + codecLabel(source.Audio[decision.Stream.AudioTrack].Codec) + " 音频，正在转换音频"
	case mediaplay.OtherTrack:
		return "切换音轨需要转换封装"
	case mediaplay.Picture:
		return "浏览器无法解码此视频，正在实时转码"
	case mediaplay.Lowered:
		return "已切换到 " + choice.Lower.Name
	}
	return ""
}

func availableQualities(info MediaInfo) []string {
	names := []string{QualityOriginal}
	for _, candidate := range mediaplay.Qualities {
		if info.Video == nil || info.Video.Height == 0 || info.Video.Height > candidate.Height {
			names = append(names, candidate.Name)
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
	if _, ok := mediaplay.QualityNamed(chosen); !ok && chosen != QualityOriginal {
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
	source, choice := playSource(info, video.FileName), playChoice(audio, chosen)
	decision := mediaplay.Decide(source, caps, choice)
	playback.Mode, playback.Reason, playback.Audio = decision.Mode, reason(decision, source, choice), decision.Stream.AudioTrack
	if decision.Mode != ModeDirect {
		playback.Mime = mediaplay.StreamMime(source, decision.Stream)
	}
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
// MaxStreams run at once, and a seek's stream waits up to StreamWait for the
// one it replaces to let go; closing the stream ends FFmpeg.
func (s *Service) Stream(ctx context.Context, actor accounts.User, videoID string, request StreamRequest) (io.ReadCloser, error) {
	if s.processor == nil {
		return nil, ErrUnavailable
	}
	video, _, err := s.visibleVideo(ctx, actor, videoID)
	if err != nil {
		return nil, err
	}
	if !mediaplay.StartWithin(request.Start, video.Duration) {
		return nil, ErrInvalid
	}
	info, err := s.mediaInfo(ctx, videoID)
	if err != nil {
		return nil, err
	}
	if request.Quality == "" {
		request.Quality = QualityOriginal
	}
	choice := playChoice(request.Audio, request.Quality)
	choice.Transcode = request.Mode == ModeTranscode
	options := StreamOptions(mediaplay.Converting(playSource(info, video.FileName), request.Caps, choice).Stream)
	options.Start = request.Start
	release, err := s.streams.Take(ctx)
	if errors.Is(err, mediaplay.ErrBusy) {
		return nil, ErrBusy
	}
	if err != nil {
		return nil, err
	}
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
	defer func() { _ = content.Reader.Close() }()
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
	defer func() { _ = content.Reader.Close() }()
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
