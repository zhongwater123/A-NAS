package photos

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/zhongwater123/A-NAS/internal/decodepanic"
)

// Content objects are immutable originals stored once per SHA-256 under
// objects/<2 hex>/<2 hex>/<64 hex>. An import streams into staging/, is
// flushed, and is then hard-linked into place, so an object path only ever
// names complete bytes.

type stagedObject struct {
	name      string
	id        string
	size      int64
	mediaType string
	metadata  photoMetadata
	// video is what the Media Worker read from a video original.
	video *videoFacts
}

var (
	jpegSignature = []byte{0xff, 0xd8, 0xff}
	pngSignature  = []byte("\x89PNG\r\n\x1a\n")
	ebmlSignature = []byte{0x1a, 0x45, 0xdf, 0xa3}
)

// sniffHeader is how much of an original the formats are told apart by:
// an MPEG transport stream shows itself only by its second packet.
const sniffHeader = 512

// sniffMediaType names the format an original's first bytes declare.
// Videos are only candidates here; the Media Worker decides whether they
// hold a video at all (docs/specs/photo-video.md).
func sniffMediaType(header []byte) string {
	box, brand := "", ""
	if len(header) >= 8 {
		box = string(header[4:8])
	}
	if len(header) >= 12 {
		brand = string(header[8:12])
	}
	switch {
	case bytes.HasPrefix(header, jpegSignature):
		return "image/jpeg"
	case bytes.HasPrefix(header, pngSignature):
		return "image/png"
	case box == "ftyp" && (brand == "heic" || brand == "heix" || brand == "mif1"):
		return "image/heic"
	// AVIF, HEIF image sequences and Canon CR3 raw photos are not videos.
	case box == "ftyp" && (brand == "" || brand == "avif" || brand == "avis" || brand == "msf1" || brand == "crx "):
		return ""
	case box == "ftyp" && brand == "qt  ":
		return "video/quicktime"
	case box == "ftyp" && strings.HasPrefix(brand, "3g"):
		return "video/3gpp"
	case box == "ftyp":
		return "video/mp4"
	// QuickTime files from before the ftyp box start with one of these.
	case box == "moov" || box == "mdat" || box == "wide" || box == "free" || box == "skip" || box == "pnot":
		return "video/quicktime"
	case bytes.HasPrefix(header, ebmlSignature):
		if bytes.Contains(header[:min(len(header), 64)], []byte("webm")) {
			return "video/webm"
		}
		return "video/x-matroska"
	case len(header) >= 12 && string(header[0:4]) == "RIFF" && string(header[8:12]) == "AVI ":
		return "video/x-msvideo"
	// A transport stream syncs every 188 bytes; a camcorder's M2TS puts a
	// 4-byte timecode before each packet.
	case len(header) > 188 && header[0] == 0x47 && header[188] == 0x47,
		len(header) > 196 && header[4] == 0x47 && header[196] == 0x47:
		return "video/mp2t"
	default:
		return ""
	}
}

func isVideoType(mediaType string) bool { return strings.HasPrefix(mediaType, "video/") }

func objectPath(id string) string { return filepath.Join(objectsDir, id[0:2], id[2:4], id) }

// stage streams an original, a photo or a video, into a new staging file,
// hashing it and enforcing the size limits, capacity reserve and supported
// formats on the way.
func (s *Service) stage(content io.Reader) (stagedObject, error) {
	return s.stageContent(content, false)
}

// stageContent stages an original or, with clip, the video of a Live Photo
// or motion photo, which must be a QuickTime or MP4 file within the photo
// limit.
func (s *Service) stageContent(content io.Reader, clip bool) (staged stagedObject, err error) {
	name := filepath.Join(stagingDir, s.randomHex()+".part")
	s.stagingMu.Lock()
	s.staging[name] = struct{}{}
	s.stagingMu.Unlock()
	file, err := s.root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		s.forgetStaging(name)
		return stagedObject{}, err
	}
	defer func() {
		if err != nil {
			_ = file.Close()
			s.discardStaging(name)
		}
	}()
	staged.name = name
	hash := sha256.New()
	header := make([]byte, 0, sniffHeader)
	buffer := make([]byte, 1<<20)
	limit := s.maxImportBytes
	for {
		n, readErr := content.Read(buffer)
		if n > 0 {
			chunk := buffer[:n]
			if staged.mediaType == "" {
				header = append(header, chunk[:min(n, cap(header)-len(header))]...)
				if len(header) == cap(header) {
					if staged.mediaType, err = classify(header, clip); err != nil {
						return stagedObject{}, err
					}
					if isVideoType(staged.mediaType) && !clip {
						limit = s.maxVideoBytes
					}
				}
			}
			if staged.size += int64(n); staged.size > limit {
				return stagedObject{}, ErrTooLarge
			}
			if err := s.ensureCapacity(int64(n)); err != nil {
				return stagedObject{}, err
			}
			hash.Write(chunk)
			if _, err := file.Write(chunk); err != nil {
				return stagedObject{}, err
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return stagedObject{}, readErr
		}
	}
	if staged.mediaType == "" {
		// Shorter than the header formats are told apart by.
		if staged.mediaType, err = classify(header, clip); err != nil {
			return stagedObject{}, err
		}
	}
	if err := file.Sync(); err != nil {
		return stagedObject{}, err
	}
	if err := file.Close(); err != nil {
		return stagedObject{}, err
	}
	if !isVideoType(staged.mediaType) {
		staged.metadata, err = s.readMetadata(staged)
	}
	if err != nil {
		return stagedObject{}, err
	}
	staged.id = hex.EncodeToString(hash.Sum(nil))
	return staged, nil
}

// classify names the format of an original, or of a clip, from its first
// bytes. A clip is always a QuickTime or MP4 file; the Media Worker reads
// it before it is accepted.
func classify(header []byte, clip bool) (string, error) {
	if clip {
		if len(header) < 12 || string(header[4:8]) != "ftyp" {
			return "", ErrInvalidMotion
		}
		return "video/quicktime", nil
	}
	mediaType := sniffMediaType(header)
	if mediaType == "" {
		return "", ErrUnsupportedType
	}
	return mediaType, nil
}

// readMetadata reads only the image header and EXIF: it proves the bytes are
// the declared format, bounds the pixel count before anything decodes them,
// and records what the timeline needs immediately. A header the decoder
// panics on is an unsupported upload.
func (s *Service) readMetadata(staged stagedObject) (metadata photoMetadata, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			decodepanic.Log("photo import", recovered)
			metadata, err = photoMetadata{}, ErrUnsupportedType
		}
	}()
	file, err := s.root.Open(staged.name)
	if err != nil {
		return photoMetadata{}, err
	}
	defer func() { _ = file.Close() }()
	if staged.mediaType == "image/heic" {
		return readHEICMetadata(file, s.location)
	}
	config, format, err := image.DecodeConfig(file)
	if err != nil || "image/"+format != staged.mediaType || config.Width <= 0 || config.Height <= 0 {
		return photoMetadata{}, ErrUnsupportedType
	}
	if int64(config.Width)*int64(config.Height) > maxImagePixels {
		return photoMetadata{}, ErrTooLarge
	}
	metadata = photoMetadata{width: config.Width, height: config.Height}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return photoMetadata{}, err
	}
	metadata.orientation, metadata.takenAt = readEXIF(file, s.location)
	return metadata, nil
}

func (s *Service) forgetStaging(name string) {
	s.stagingMu.Lock()
	delete(s.staging, name)
	s.stagingMu.Unlock()
}

func (s *Service) discardStaging(name string) {
	_ = s.root.Remove(name)
	s.forgetStaging(name)
}

// publish makes the staged bytes available as content object staged.id and
// reports whether this call created the object. The caller holds commitMu.
func (s *Service) publish(staged stagedObject) (bool, error) {
	target := objectPath(staged.id)
	if err := s.ensureObjectDir(filepath.Dir(target)); err != nil {
		return false, err
	}
	if err := s.root.Chmod(staged.name, 0o400); err != nil {
		return false, err
	}
	err := s.root.Link(staged.name, target)
	if errors.Is(err, fs.ErrExist) {
		info, statErr := s.root.Lstat(target)
		if statErr != nil {
			return false, statErr
		}
		if !info.Mode().IsRegular() || info.Size() != staged.size {
			return false, fmt.Errorf("content object %s does not match its hash", staged.id)
		}
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := s.syncDir(filepath.Dir(target)); err != nil {
		_ = s.root.Remove(target)
		return false, err
	}
	return true, nil
}

func (s *Service) ensureObjectDir(dir string) error {
	for _, level := range []string{filepath.Dir(dir), dir} {
		err := s.root.Mkdir(level, 0o700)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return err
		}
		if err := s.syncDir(filepath.Dir(level)); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) syncDir(name string) error {
	dir, err := s.root.Open(name)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	return dir.Sync()
}

func (s *Service) removeObject(id string) error {
	err := s.root.Remove(objectPath(id))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

func (s *Service) openObject(id string) (*os.File, error) { return s.root.Open(objectPath(id)) }

// ensureCapacity keeps the same data-volume reserve as ordinary files.
func (s *Service) ensureCapacity(incoming int64) error {
	ok, err := s.admits(s.rootPath, incoming)
	if err != nil {
		return err
	}
	if !ok {
		return ErrInsufficientSpace
	}
	return nil
}
