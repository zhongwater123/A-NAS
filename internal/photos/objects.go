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
	"syscall"
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
}

var (
	jpegSignature = []byte{0xff, 0xd8, 0xff}
	pngSignature  = []byte("\x89PNG\r\n\x1a\n")
)

func sniffMediaType(header []byte) string {
	switch {
	case bytes.HasPrefix(header, jpegSignature):
		return "image/jpeg"
	case bytes.HasPrefix(header, pngSignature):
		return "image/png"
	default:
		return ""
	}
}

func objectPath(id string) string { return filepath.Join(objectsDir, id[0:2], id[2:4], id) }

// stage streams content into a new staging file, hashing it and enforcing the
// size limit, capacity reserve and supported formats on the way.
func (s *Service) stage(content io.Reader) (staged stagedObject, err error) {
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
	header := make([]byte, 0, len(pngSignature))
	buffer := make([]byte, 1<<20)
	for {
		n, readErr := content.Read(buffer)
		if n > 0 {
			chunk := buffer[:n]
			if staged.size += int64(n); staged.size > s.maxImportBytes {
				return stagedObject{}, ErrTooLarge
			}
			if staged.mediaType == "" {
				header = append(header, chunk[:min(n, cap(header)-len(header))]...)
				if len(header) == cap(header) {
					if staged.mediaType = sniffMediaType(header); staged.mediaType == "" {
						return stagedObject{}, ErrUnsupportedType
					}
				}
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
		return stagedObject{}, ErrUnsupportedType
	}
	if err := file.Sync(); err != nil {
		return stagedObject{}, err
	}
	if err := file.Close(); err != nil {
		return stagedObject{}, err
	}
	if err := s.checkImage(staged); err != nil {
		return stagedObject{}, err
	}
	staged.id = hex.EncodeToString(hash.Sum(nil))
	return staged, nil
}

// checkImage reads only the image header: it proves the bytes are the
// declared format and bounds the pixel count before anything decodes them.
func (s *Service) checkImage(staged stagedObject) error {
	file, err := s.root.Open(staged.name)
	if err != nil {
		return err
	}
	defer file.Close()
	config, format, err := image.DecodeConfig(file)
	if err != nil || "image/"+format != staged.mediaType || config.Width <= 0 || config.Height <= 0 {
		return ErrUnsupportedType
	}
	if int64(config.Width)*int64(config.Height) > maxImagePixels {
		return ErrTooLarge
	}
	return nil
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
	defer dir.Close()
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

// ensureCapacity keeps the same data-volume reserve as ordinary files: 5% of
// the volume and at least 10 GiB stay free.
func (s *Service) ensureCapacity(incoming int64) error {
	if s.disableCapacityReserve {
		return nil
	}
	var stats syscall.Statfs_t
	if err := syscall.Statfs(s.rootPath, &stats); err != nil {
		return err
	}
	total := uint64(stats.Blocks) * uint64(stats.Bsize)
	available := uint64(stats.Bavail) * uint64(stats.Bsize)
	reserve := max(total/20, uint64(10<<30))
	if uint64(max(incoming, 0)) >= available || available-uint64(max(incoming, 0)) < reserve {
		return ErrInsufficientSpace
	}
	return nil
}
