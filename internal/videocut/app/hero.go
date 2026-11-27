package app

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/zhongwater123/A-NAS/internal/videocut/domain"
)

// The home page's background film, as the 第八版 prototype's hero.mp4: a
// device may put one under the store's root, hero/hero.mp4 with
// hero/hero.jpg as its poster. It is the device's own footage, never part
// of the software (the repository is public); without it the page plays
// the member's videos (docs/architecture/videocut-center.md, 网页).
const heroDir = "hero"

// Hero files by what the page asks for.
var heroFiles = map[string]string{"film": "hero.mp4", "poster": "hero.jpg"}

// OpenHero opens the hero's film or poster; ErrNotFound when the device has none.
func (s *Store) OpenHero(which string) (domain.HeroFile, error) {
	name, ok := heroFiles[which]
	if !ok {
		return domain.HeroFile{}, domain.ErrNotFound
	}
	file, err := os.Open(filepath.Join(s.root, heroDir, name))
	if errors.Is(err, fs.ErrNotExist) {
		return domain.HeroFile{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.HeroFile{}, err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = file.Close()
		return domain.HeroFile{}, errors.Join(domain.ErrNotFound, err)
	}
	kind := "video/mp4"
	if which == "poster" {
		kind = "image/jpeg"
	}
	return domain.HeroFile{File: file, Info: info, ContentType: kind}, nil
}
