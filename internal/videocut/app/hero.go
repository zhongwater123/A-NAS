package app

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// The home page's background film, as the 第八版 prototype's hero.mp4: a
// device may put one under the store's root, hero/hero.mp4 with
// hero/hero.jpg as its poster. It is the device's own footage, never part
// of the software (the repository is public); without it the page plays
// the member's videos (docs/architecture/montage-center.md, 网页).
const heroDir = "hero"

// Hero files by what the page asks for.
var heroFiles = map[string]string{"film": "hero.mp4", "poster": "hero.jpg"}

// HeroFile is a hero file opened: its content type and what serving it needs.
type HeroFile struct {
	File        *os.File
	Info        fs.FileInfo
	ContentType string
}

// OpenHero opens the hero's film or poster; ErrNotFound when the device has none.
func (s *Store) OpenHero(which string) (HeroFile, error) {
	name, ok := heroFiles[which]
	if !ok {
		return HeroFile{}, ErrNotFound
	}
	file, err := os.Open(filepath.Join(s.root, heroDir, name))
	if errors.Is(err, fs.ErrNotExist) {
		return HeroFile{}, ErrNotFound
	}
	if err != nil {
		return HeroFile{}, err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = file.Close()
		return HeroFile{}, errors.Join(ErrNotFound, err)
	}
	kind := "video/mp4"
	if which == "poster" {
		kind = "image/jpeg"
	}
	return HeroFile{File: file, Info: info, ContentType: kind}, nil
}
