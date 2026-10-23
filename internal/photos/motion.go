package photos

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"math"
	"path/filepath"
	"strings"
	"time"

	"github.com/zhongwater123/A-NAS/internal/mediaworker"
)

var ErrInvalidMotion = errors.New("动态照片无效：请将同名照片与 MOV 一起上传，并保留原始配对信息；视频须完整且不超过 30 秒")

type motionImport struct {
	object     stagedObject
	name, kind string
	duration   float64
}

func (s *Service) prepareMotion(ctx context.Context, photo stagedObject, request ImportRequest) (_ *motionImport, err error) {
	source, err := s.root.Open(photo.name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = source.Close() }()
	var reader io.Reader
	name, kind := request.MotionName, "live-photo"
	if request.MotionContent != nil {
		if _, ok := cleanName(name); !ok || !strings.EqualFold(filepath.Ext(name), ".mov") || !strings.EqualFold(strings.TrimSuffix(name, filepath.Ext(name)), strings.TrimSuffix(request.Name, filepath.Ext(request.Name))) {
			return nil, ErrInvalidMotion
		}
		reader = request.MotionContent
	} else {
		offset, length, err := motionRange(source, photo.size, photo.mediaType)
		if err != nil {
			return nil, err
		}
		if length == 0 {
			return nil, nil
		}
		reader = io.NewSectionReader(source, offset, length)
		name = strings.TrimSuffix(request.Name, filepath.Ext(request.Name)) + ".mp4"
		kind = "motion-photo"
	}
	if s.media == nil {
		return nil, mediaworker.ErrUnavailable
	}
	clip, err := s.stageContent(reader, true)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			s.discardStaging(clip.name)
		}
	}()
	probe, err := s.probeMotion(ctx, clip)
	if err != nil {
		return nil, err
	}
	if kind == "live-photo" {
		stillID := appleContentID(source)
		videoID := probe.Format.Tags["com.apple.quicktime.content.identifier"]
		// Some exports strip both identifiers. An explicit, unambiguous same-name
		// pair is accepted then; an available but unmatched identifier is never ignored.
		if (stillID != "" || videoID != "") && (stillID == "" || videoID == "" || stillID != videoID) {
			return nil, ErrInvalidMotion
		}
	} else {
		clip.mediaType = "video/mp4"
	}
	return &motionImport{object: clip, name: name, kind: kind, duration: probe.Format.Duration}, nil
}

// probeMotion reads a staged clip's format through the Media Worker and
// accepts only a short, complete video.
func (s *Service) probeMotion(ctx context.Context, clip stagedObject) (mediaworker.Probe, error) {
	file, err := s.root.Open(clip.name)
	if err != nil {
		return mediaworker.Probe{}, err
	}
	defer func() { _ = file.Close() }()
	probe, err := s.media.Probe(mediaworker.WithTimeout(ctx, 15*time.Second), file)
	if err != nil {
		return mediaworker.Probe{}, err
	}
	if !validMotionProbe(probe) {
		return mediaworker.Probe{}, ErrInvalidMotion
	}
	return probe, nil
}

func validMotionProbe(p mediaworker.Probe) bool {
	if math.IsNaN(p.Format.Duration) || math.IsInf(p.Format.Duration, 0) || p.Format.Duration <= 0 || p.Format.Duration > 30 {
		return false
	}
	for _, stream := range p.Streams {
		if stream.Type == "video" && !stream.AttachedPic && stream.Width > 0 && stream.Height > 0 && int64(stream.Width) <= maxImagePixels/int64(stream.Height) {
			return true
		}
	}
	return false
}

func (s *Service) insertMotion(ctx context.Context, tx *sql.Tx, id string, m *motionImport) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO objects(id,size_bytes,media_type,created_at) VALUES(?,?,?,?) ON CONFLICT(id) DO NOTHING`, m.object.id, m.object.size, m.object.mediaType, formatTime(s.now())); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE assets SET motion_object_id=?,motion_name=?,motion_kind=?,motion_duration=? WHERE id=?`, m.object.id, m.name, m.kind, m.duration, id)
	return err
}

// OpenMotion applies the same asset Policy as the primary still on every call.
func (s *Service) OpenMotion(ctx context.Context, p Principal, id string) (Content, error) {
	record, err := s.visibleAsset(ctx, s.db, p, id)
	if err != nil {
		return Content{}, err
	}
	if record.Motion == nil {
		return Content{}, ErrNotFound
	}
	file, err := s.openObject(record.motionObjectID)
	if err != nil {
		return Content{}, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return Content{}, err
	}
	mediaType := "video/quicktime"
	if record.Motion.Kind == "motion-photo" {
		mediaType = "video/mp4"
	}
	return Content{Name: record.motionName, MediaType: mediaType, SizeBytes: info.Size(), ETag: `"` + record.motionObjectID + `"`, Reader: file}, nil
}

type motionStream struct {
	io.ReadCloser
	cancel  context.CancelFunc
	release func()
}

func (s motionStream) Close() error { s.cancel(); err := s.ReadCloser.Close(); s.release(); return err }

// PlayMotion produces a browser-compatible H.264/AAC stream only on demand.
// The request owns the decoder; closing it cancels work and frees the
// worker. A clip lasts seconds and someone is watching, so it is interactive
// rather than playback that may keep its worker for hours.
func (s *Service) PlayMotion(ctx context.Context, p Principal, id string) (io.ReadCloser, error) {
	content, err := s.OpenMotion(ctx, p, id)
	if err != nil {
		return nil, err
	}
	defer func() { _ = content.Reader.Close() }()
	if s.media == nil {
		return nil, mediaworker.ErrUnavailable
	}
	ctx = mediaworker.WithTimeout(mediaworker.WithPriority(ctx, mediaworker.Interactive), 45*time.Second)
	return s.media.Stream(ctx, content.Reader, mediaworker.StreamRequest{Height: 1080, MaxBitrate: 4000, AudioTrack: 0})
}
