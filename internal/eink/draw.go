package eink

import (
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
)

// The picture a screen draws (docs/specs/eink-screens.md, “设备与接口”):
// 800×480, one bit a pixel. The offline bar covers the bottom left, where
// the board's footer is, and leaves the companion's column alone.
const (
	Width      = 800
	Height     = 480
	BarWidth   = 592
	BarHeight  = 80
	PictureLen = Width * Height / 8
	BarLen     = BarWidth * BarHeight / 8
)

// FontDir is where Debian's fonts-noto-cjk keeps Noto Sans CJK, which the
// a-nas package depends on; the screens draw with its Simplified Chinese
// face. Nothing of the font is in the repository.
const FontDir = "/usr/share/fonts/opentype/noto"

// Row is one line of the board: what to do and how long is left.
type Row struct {
	Text   string
	When   string
	Urgent bool
}

// Fonts draws pictures for screens.
type Fonts struct {
	regular, bold *sfnt.Font
}

// LoadFonts reads Noto Sans CJK from dir and picks its Simplified Chinese
// face in both weights.
func LoadFonts(dir string) (*Fonts, error) {
	regular, err := cjkFace(filepath.Join(dir, "NotoSansCJK-Regular.ttc"))
	if err != nil {
		return nil, err
	}
	bold, err := cjkFace(filepath.Join(dir, "NotoSansCJK-Bold.ttc"))
	if err != nil {
		return nil, err
	}
	return &Fonts{regular: regular, bold: bold}, nil
}

func cjkFace(path string) (*sfnt.Font, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("e-ink font: %w", err)
	}
	collection, err := sfnt.ParseCollection(data)
	if err != nil {
		return nil, fmt.Errorf("e-ink font %s: %w", path, err)
	}
	var buffer sfnt.Buffer
	for i := range collection.NumFonts() {
		face, err := collection.Font(i)
		if err != nil {
			return nil, fmt.Errorf("e-ink font %s: %w", path, err)
		}
		if name, err := face.Name(&buffer, sfnt.NameIDFamily); err == nil && strings.HasSuffix(name, " SC") {
			return face, nil
		}
	}
	return nil, errors.New("e-ink font " + path + " has no Simplified Chinese face")
}

func (f *Fonts) face(bold bool, size float64) font.Face {
	source := f.regular
	if bold {
		source = f.bold
	}
	face, err := opentype.NewFace(source, &opentype.FaceOptions{Size: size, DPI: 72, Hinting: font.HintingNone})
	if err != nil {
		panic(err) // only for a size the code gives wrongly
	}
	return face
}

func width(face font.Face, text string) int { return font.MeasureString(face, text).Ceil() }

// text draws s with its baseline at y.
func text(dst draw.Image, face font.Face, ink color.Gray, x, y int, s string) {
	(&font.Drawer{Dst: dst, Src: image.NewUniform(ink), Face: face, Dot: fixed.P(x, y)}).DrawString(s)
}

// fit is the largest size from start down to low at which s fits in room.
func (f *Fonts) fit(bold bool, start, low float64, s string, room int) font.Face {
	for size := start; ; size -= 2 {
		face := f.face(bold, size)
		if size <= low || width(face, s) <= room {
			return face
		}
	}
}

var (
	white = color.Gray{Y: 0xff}
	black = color.Gray{Y: 0}
)

// Board layout: the left column holds the rows and the footer, the right
// column is the companion's.
const (
	margin    = 32
	leftWidth = BarWidth
	rowTop    = 40
	rowHeight = 112
	footerY   = 448
)

// Board draws the to-do board: up to three rows, the most pressing first,
// urgent ones white on black; the rest counted at the bottom with the time
// of the update; 都处理完了 when nothing is listed.
func (f *Fonts) Board(rows []Row, more int, empty string, now time.Time) *image.Gray {
	picture := image.NewGray(image.Rect(0, 0, Width, Height))
	draw.Draw(picture, picture.Bounds(), image.NewUniform(white), image.Point{}, draw.Src)
	if len(rows) == 0 {
		face := f.face(true, 56)
		text(picture, face, black, (leftWidth-width(face, empty))/2, 230, empty)
	}
	for i, row := range rows {
		top := rowTop + i*rowHeight
		ink := black
		if row.Urgent {
			draw.Draw(picture, image.Rect(margin-12, top, leftWidth-12, top+rowHeight-16), image.NewUniform(black), image.Point{}, draw.Src)
			ink = white
		}
		when := f.face(true, 40)
		whenWidth := width(when, row.When)
		title := f.fit(true, 56, 32, row.Text, leftWidth-margin*2-whenWidth-24)
		baseline := top + rowHeight/2 + 12
		text(picture, title, ink, margin, baseline, row.Text)
		text(picture, when, ink, leftWidth-margin-whenWidth, baseline, row.When)
		if i < len(rows)-1 && !row.Urgent && !rows[i+1].Urgent {
			for x := margin; x < leftWidth-margin; x += 8 {
				draw.Draw(picture, image.Rect(x, top+rowHeight-8, x+3, top+rowHeight-6), image.NewUniform(black), image.Point{}, draw.Src)
			}
		}
	}
	small := f.face(false, 28)
	if more > 0 {
		text(picture, small, black, margin, footerY, "另有 "+strconv.Itoa(more)+" 件")
	}
	updated := "更新 " + now.Format("15:04")
	text(picture, small, black, leftWidth-margin-width(small, updated), footerY, updated)
	return picture
}

// Bar draws the offline bar: white on black, 停止更新 · 画面停在 M月D日 HH:MM,
// the time being when the screen last heard from the NAS.
func (f *Fonts) Bar(now time.Time) *image.Gray {
	bar := image.NewGray(image.Rect(0, 0, BarWidth, BarHeight))
	draw.Draw(bar, bar.Bounds(), image.NewUniform(black), image.Point{}, draw.Src)
	words := fmt.Sprintf("停止更新 · 画面停在 %d月%d日 %s", int(now.Month()), now.Day(), now.Format("15:04"))
	face := f.fit(true, 32, 20, words, BarWidth-margin*2)
	text(bar, face, white, margin, BarHeight/2+11, words)
	return bar
}

// Pack turns a picture into the bytes a screen draws: rows top to bottom,
// pixels left to right, the high bit first, 1 white and 0 black. Grey that
// the font's edges leave is white from half way up.
func Pack(picture *image.Gray) []byte {
	bounds := picture.Bounds()
	out := make([]byte, bounds.Dx()*bounds.Dy()/8)
	for y := range bounds.Dy() {
		for x := range bounds.Dx() {
			if picture.GrayAt(bounds.Min.X+x, bounds.Min.Y+y).Y >= 0x80 {
				i := y*bounds.Dx() + x
				out[i/8] |= 0x80 >> (i % 8)
			}
		}
	}
	return out
}
