// Package media is the media center (docs/specs/media-center.md): libraries
// over folders of ordinary files, a catalog of films, shows and other videos
// read from names, NFO files and FFmpeg, and each user's progress, favorites
// and collections.
//
// It never reads the data volume itself. Every file is opened through
// files.Service as a signed-in user, so in production the File Broker opens
// it with that user's identity and the kernel's ACLs decide (ADR 0017).
// FFmpeg runs behind a Processor, which production connects to a sandboxed
// worker that receives only the opened descriptor.
package media

import (
	"context"
	"errors"
	"io"
	"os"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accounts"
)

var (
	ErrNotFound  = errors.New("media resource not found")
	ErrForbidden = errors.New("media resource is forbidden")
	ErrInvalid   = errors.New("media request is invalid")
	ErrConflict  = errors.New("media library folders overlap")
	// ErrBusy reports that every transcoding slot is in use.
	ErrBusy = errors.New("media transcoding is busy")
	// ErrUnavailable reports that no FFmpeg processor is configured or the
	// worker cannot be reached.
	ErrUnavailable = errors.New("media processor is unavailable")
	// ErrUnreadable reports that FFmpeg cannot read the file.
	ErrUnreadable = errors.New("media file cannot be read")
)

type LibraryKind string

const (
	LibraryMovies LibraryKind = "movies"
	LibraryShows  LibraryKind = "shows"
	LibraryMixed  LibraryKind = "mixed"
	LibraryOther  LibraryKind = "other"
)

type TitleType string

const (
	TypeMovie   TitleType = "movie"
	TypeShow    TitleType = "show"
	TypeEpisode TitleType = "episode"
	TypeOther   TitleType = "other"
)

type Library struct {
	ID          string             `json:"id"`
	Name        string             `json:"name"`
	Kind        LibraryKind        `json:"kind"`
	SpaceID     string             `json:"spaceId"`
	SpaceKind   accounts.SpaceKind `json:"spaceKind"`
	OwnerUserID string             `json:"ownerUserId,omitempty"`
	CreatedBy   string             `json:"createdBy"`
	CreatedAt   time.Time          `json:"createdAt"`
	Folders     []LibraryFolder    `json:"folders"`
	Counts      LibraryCounts      `json:"counts"`
	Scan        ScanStatus         `json:"scan"`
	CanManage   bool               `json:"canManage"`
	// Covers are up to four titles with artwork, for the library card.
	Covers []string `json:"covers"`
}

type LibraryFolder struct {
	EntryID string `json:"entryId"`
	Name    string `json:"name"`
	// Path is slash-separated within the space.
	Path    string `json:"path"`
	Missing bool   `json:"missing,omitempty"`
}

type LibraryCounts struct {
	Movies    int   `json:"movies"`
	Shows     int   `json:"shows"`
	Episodes  int   `json:"episodes"`
	Others    int   `json:"others"`
	SizeBytes int64 `json:"sizeBytes"`
}

type ScanStatus struct {
	// State is idle, scanning (listing folders) or processing (reading
	// durations and taking frames).
	State     string     `json:"state"`
	Pending   int        `json:"pending"`
	ScannedAt *time.Time `json:"scannedAt,omitempty"`
	Error     string     `json:"error,omitempty"`
}

type LibraryInput struct {
	Name      string      `json:"name"`
	Kind      LibraryKind `json:"kind"`
	SpaceID   string      `json:"spaceId"`
	FolderIDs []string    `json:"folderIds"`
}

// Artwork says which images a title has; the client asks for them by kind.
type Artwork struct {
	Poster   bool   `json:"poster"`
	Backdrop bool   `json:"backdrop"`
	Thumb    bool   `json:"thumb"`
	Version  string `json:"version,omitempty"`
}

type Progress struct {
	Position float64    `json:"position"`
	Duration float64    `json:"duration"`
	Watched  bool       `json:"watched"`
	PlayedAt *time.Time `json:"playedAt,omitempty"`
}

// Title is a card: a film, a show or another video.
type Title struct {
	ID            string    `json:"id"`
	Type          TitleType `json:"type"`
	LibraryID     string    `json:"libraryId"`
	Title         string    `json:"title"`
	OriginalTitle string    `json:"originalTitle,omitempty"`
	Year          int       `json:"year,omitempty"`
	Genres        []string  `json:"genres,omitempty"`
	Rating        float64   `json:"rating,omitempty"`
	AddedAt       time.Time `json:"addedAt"`
	Duration      float64   `json:"duration,omitempty"`
	Resolution    string    `json:"resolution,omitempty"`
	HDR           string    `json:"hdr,omitempty"`
	Artwork       Artwork   `json:"artwork"`
	Favorite      bool      `json:"favorite"`
	Progress      *Progress `json:"progress,omitempty"`
	// Shows only.
	Seasons  int `json:"seasons,omitempty"`
	Episodes int `json:"episodes,omitempty"`
	Watched  int `json:"watchedEpisodes,omitempty"`
	// Episodes only, where a list shows them on their own.
	ShowID    string `json:"showId,omitempty"`
	ShowTitle string `json:"showTitle,omitempty"`
	Season    int    `json:"season,omitempty"`
	Episode   int    `json:"episode,omitempty"`
}

type TitlePage struct {
	Items []Title `json:"items"`
	Total int     `json:"total"`
}

type Home struct {
	Featured  []Title `json:"featured"`
	Continue  []Title `json:"continue"`
	Recent    []Title `json:"recent"`
	Movies    []Title `json:"movies"`
	Shows     []Title `json:"shows"`
	Others    []Title `json:"others"`
	Favorites []Title `json:"favorites"`
	Libraries int     `json:"libraries"`
	Scanning  bool    `json:"scanning"`
}

type Episode struct {
	ID       string    `json:"id"`
	Season   int       `json:"season"`
	Episode  int       `json:"episode"`
	Title    string    `json:"title"`
	Plot     string    `json:"plot,omitempty"`
	Duration float64   `json:"duration,omitempty"`
	AddedAt  time.Time `json:"addedAt"`
	Artwork  Artwork   `json:"artwork"`
	Progress *Progress `json:"progress,omitempty"`
}

type Season struct {
	Number   int       `json:"number"`
	Episodes []Episode `json:"episodes"`
}

type ShowDetail struct {
	Title
	Plot string `json:"plot,omitempty"`
	// Next is the episode the play button starts.
	Next        *Episode        `json:"next,omitempty"`
	SeasonList  []Season        `json:"seasonList"`
	Collections []CollectionRef `json:"collections"`
}

type CollectionRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type VideoFile struct {
	Name       string    `json:"name"`
	Folder     string    `json:"folder"`
	SizeBytes  int64     `json:"sizeBytes"`
	ModifiedAt time.Time `json:"modifiedAt"`
}

type SubtitleTrack struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Language string `json:"language,omitempty"`
	External bool   `json:"external"`
	Default  bool   `json:"default,omitempty"`
}

type AudioTrack struct {
	Index    int    `json:"index"`
	Label    string `json:"label"`
	Language string `json:"language,omitempty"`
	Codec    string `json:"codec"`
	Channels int    `json:"channels,omitempty"`
	Default  bool   `json:"default,omitempty"`
}

type VideoDetail struct {
	Title
	Plot        string          `json:"plot,omitempty"`
	EpisodeName string          `json:"episodeTitle,omitempty"`
	File        VideoFile       `json:"file"`
	Media       *MediaSummary   `json:"media,omitempty"`
	ProbeState  string          `json:"probeState"`
	Audio       []AudioTrack    `json:"audio"`
	Subtitles   []SubtitleTrack `json:"subtitles"`
	Previous    string          `json:"previousId,omitempty"`
	Next        string          `json:"nextId,omitempty"`
	Collections []CollectionRef `json:"collections"`
	Collection  string          `json:"collection,omitempty"`
}

type MediaSummary struct {
	Container  string  `json:"container"`
	VideoCodec string  `json:"videoCodec,omitempty"`
	AudioCodec string  `json:"audioCodec,omitempty"`
	Width      int     `json:"width,omitempty"`
	Height     int     `json:"height,omitempty"`
	FrameRate  float64 `json:"frameRate,omitempty"`
	Bitrate    int64   `json:"bitrate,omitempty"`
}

type Collection struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Automatic bool      `json:"automatic"`
	Count     int       `json:"count"`
	UpdatedAt time.Time `json:"updatedAt"`
	Covers    []string  `json:"covers"`
}

type CollectionDetail struct {
	Collection
	Items []Title `json:"items"`
}

type FolderRoot struct {
	LibraryID   string `json:"libraryId"`
	LibraryName string `json:"libraryName"`
	EntryID     string `json:"entryId"`
	Name        string `json:"name"`
	Path        string `json:"path"`
	Videos      int    `json:"videos"`
	Cover       string `json:"cover,omitempty"`
	Missing     bool   `json:"missing,omitempty"`
}

type Folder struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	Videos int    `json:"videos"`
	Cover  string `json:"cover,omitempty"`
}

type FolderListing struct {
	Root    FolderRoot `json:"root"`
	Path    string     `json:"path"`
	Folders []Folder   `json:"folders"`
	Videos  []Title    `json:"videos"`
}

// Playback tells the player how to play a video.
type Playback struct {
	// Mode is direct (the original file, seekable), remux (video copied,
	// audio converted) or transcode (H.264 and AAC).
	Mode     string  `json:"mode"`
	URL      string  `json:"url"`
	Duration float64 `json:"duration,omitempty"`
	// Reason explains why the original file is not played directly.
	Reason  string `json:"reason,omitempty"`
	Audio   int    `json:"audio"`
	Quality string `json:"quality"`
	// Qualities lists the choices for this video's height.
	Qualities []string `json:"qualities"`
}

// Capabilities are what the browser says it can decode.
type Capabilities struct {
	HEVC, AV1, VP9, AC3, EAC3, Matroska, WebM bool
}

// MediaInfo is what FFmpeg reads from a file.
type MediaInfo struct {
	Container string           `json:"container"`
	Duration  float64          `json:"duration"`
	Bitrate   int64            `json:"bitrate,omitempty"`
	Video     *VideoStream     `json:"video,omitempty"`
	Audio     []AudioStream    `json:"audio,omitempty"`
	Subtitles []SubtitleStream `json:"subtitles,omitempty"`
}

type VideoStream struct {
	Index       int     `json:"index"`
	Codec       string  `json:"codec"`
	Profile     string  `json:"profile,omitempty"`
	Width       int     `json:"width"`
	Height      int     `json:"height"`
	PixelFormat string  `json:"pixelFormat,omitempty"`
	FrameRate   float64 `json:"frameRate,omitempty"`
	// HDR is HDR10, HLG or Dolby Vision.
	HDR string `json:"hdr,omitempty"`
}

type AudioStream struct {
	Index    int    `json:"index"`
	Codec    string `json:"codec"`
	Channels int    `json:"channels,omitempty"`
	Language string `json:"language,omitempty"`
	Title    string `json:"title,omitempty"`
	Default  bool   `json:"default,omitempty"`
}

type SubtitleStream struct {
	Index    int    `json:"index"`
	Codec    string `json:"codec"`
	Language string `json:"language,omitempty"`
	Title    string `json:"title,omitempty"`
	Default  bool   `json:"default,omitempty"`
	Forced   bool   `json:"forced,omitempty"`
}

// StreamOptions describe one converted stream, which always starts at Start
// and is fragmented MP4 with H.264 or copied video and AAC or copied audio.
type StreamOptions struct {
	Start     float64
	CopyVideo bool
	// HEVC tags copied H.265 video as hvc1, which browsers require.
	HEVC bool
	// Height and MaxBitrate (kbit/s) limit transcoded video; zero keeps the
	// source size.
	Height     int
	MaxBitrate int
	// AudioTrack is the position among the audio streams, or -1 for none.
	AudioTrack int
	CopyAudio  bool
}

// SubtitleSource selects a subtitle to convert to WebVTT: an external file
// in Format (srt, ass or webvtt) and Charset, or the embedded Stream.
type SubtitleSource struct {
	Format  string
	Charset string
	Stream  int
}

// Processor runs FFmpeg on files that are already open. The caller keeps
// ownership of file and may close it once a call returns; a stream keeps its
// own descriptor.
type Processor interface {
	Probe(ctx context.Context, file *os.File) (MediaInfo, error)
	Frame(ctx context.Context, file *os.File, at float64, width int) ([]byte, error)
	Subtitle(ctx context.Context, file *os.File, source SubtitleSource) ([]byte, error)
	Stream(ctx context.Context, file *os.File, options StreamOptions) (io.ReadCloser, error)
}
