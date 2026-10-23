package music

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/disintegration/imaging"
)

// Cover sizes. Small covers fill track rows and album grids; large ones the
// album header and the now-playing view.
const (
	CoverSmall = "small"
	CoverLarge = "large"
)

var coverPixels = map[string]int{CoverSmall: 240, CoverLarge: 720}

// maxCoverBytes bounds the folder images and embedded pictures the library
// decodes; album art is far smaller.
const maxCoverBytes = 16 << 20

// maxCoverPixels bounds decoded cover dimensions before any allocation.
const maxCoverPixels = 50_000_000

var errUndecodableCover = errors.New("cover picture cannot be decoded")

// coverStore keeps square JPEG renditions of album art on the system disk,
// named by the SHA-256 of the source picture: tracks of one album share a
// rendition, and it never needs invalidating. Covers are derived data and can
// be deleted at any time; the next scan writes them again.
type coverStore struct{ root string }

// coverNames are folder images that serve as album art, in preference order.
var coverNames = []string{"cover", "folder", "front", "album", "albumart", "albumartsmall"}

// folderCoverRank orders the images of a folder by how likely each is the album
// art; 0 means the image is not album art.
func folderCoverRank(name string) int {
	ext := strings.ToLower(path.Ext(name))
	if ext != ".jpg" && ext != ".jpeg" && ext != ".png" {
		return 0
	}
	base := strings.ToLower(strings.TrimSuffix(name, path.Ext(name)))
	for index, candidate := range coverNames {
		if base == candidate || (candidate == "albumart" && strings.HasPrefix(base, "albumart_")) {
			return len(coverNames) - index
		}
	}
	return 0
}

func (c coverStore) path(id, size string) string {
	return filepath.Join(c.root, id[:2], id+"-"+size+".jpg")
}

// Path returns the rendition file of a cover, or os.ErrNotExist.
func (c coverStore) Path(id, size string) (string, error) {
	if _, ok := coverPixels[size]; !ok || !validCoverID(id) {
		return "", os.ErrNotExist
	}
	file := c.path(id, size)
	if _, err := os.Stat(file); err != nil {
		return "", err
	}
	return file, nil
}

// Put stores the renditions of a picture and returns its cover ID. A picture
// stored before is not decoded again. A decoder panic counts as an
// undecodable picture: covers are decoded by background scans, and the same
// bytes would panic again on every scan.
func (c coverStore) Put(picture []byte) (id string, err error) {
	defer func() {
		if recover() != nil {
			id, err = "", errUndecodableCover
		}
	}()
	if len(picture) == 0 || len(picture) > maxCoverBytes {
		return "", errors.New("cover picture size is out of range")
	}
	sum := sha256.Sum256(picture)
	id = hex.EncodeToString(sum[:16])
	if _, err := c.Path(id, CoverLarge); err == nil {
		if _, err := c.Path(id, CoverSmall); err == nil {
			return id, nil
		}
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(picture))
	if err != nil {
		return "", fmt.Errorf("decode cover: %w", err)
	}
	if config.Width <= 0 || config.Height <= 0 || config.Width*config.Height > maxCoverPixels {
		return "", errors.New("cover dimensions are out of range")
	}
	source, _, err := image.Decode(bytes.NewReader(picture))
	if err != nil {
		return "", fmt.Errorf("decode cover: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(c.path(id, CoverSmall)), 0o700); err != nil {
		return "", err
	}
	for size, pixels := range coverPixels {
		edge := min(pixels, config.Width, config.Height)
		square := imaging.Fill(source, edge, edge, imaging.Center, imaging.Lanczos)
		if err := writeJPEG(c.path(id, size), square); err != nil {
			return "", err
		}
	}
	return id, nil
}

func writeJPEG(target string, picture image.Image) error {
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, picture, &jpeg.Options{Quality: 86}); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(target), ".cover-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(temporary.Name()) }()
	if _, err := temporary.Write(encoded.Bytes()); err != nil {
		_ = temporary.Close()
		return err
	}
	// Put never renders a cover again once its file exists, so the file is
	// synced before it takes its name: a crash must not leave it empty.
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporary.Name(), target)
}

func validCoverID(id string) bool {
	if len(id) != 32 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}
