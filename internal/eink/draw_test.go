package eink

import (
	"bytes"
	"image"
	"os"
	"testing"
	"time"
)

// fonts loads Noto Sans CJK as the NAS has it. Development machines and CI
// install fonts-noto-cjk; without it the screens cannot draw, so the tests
// fail rather than skip.
func fonts(t *testing.T) *Fonts {
	t.Helper()
	f, err := LoadFonts(FontDir)
	if err != nil {
		t.Fatalf("%v (install the Debian package fonts-noto-cjk)", err)
	}
	return f
}

func blackIn(picture *image.Gray, area image.Rectangle) int {
	n := 0
	for y := area.Min.Y; y < area.Max.Y; y++ {
		for x := area.Min.X; x < area.Max.X; x++ {
			if picture.GrayAt(x, y).Y < 0x80 {
				n++
			}
		}
	}
	return n
}

func TestBoardPicture(t *testing.T) {
	f := fonts(t)
	now := time.Date(2026, 11, 27, 10, 5, 0, 0, time.Local)
	rows := []Row{
		{Text: "交停车费", When: "已过期 3 天", Urgent: true},
		{Text: "一件医疗的事", When: "还有 5 天"},
		{Text: "续租房合同十个字", When: "还有 45 天"},
	}
	picture := f.Board(rows, 2, "都处理完了", now)
	if picture.Bounds().Dx() != Width || picture.Bounds().Dy() != Height {
		t.Fatalf("picture is %v", picture.Bounds())
	}
	// The urgent row is white on black: mostly black.
	urgent := image.Rect(margin, rowTop+8, leftWidth-margin, rowTop+rowHeight-24)
	if black, all := blackIn(picture, urgent), urgent.Dx()*urgent.Dy(); black < all*3/4 {
		t.Errorf("the urgent row has %d of %d pixels black", black, all)
	}
	// The other rows are black on white, with words in them.
	plain := image.Rect(margin, rowTop+rowHeight+8, leftWidth-margin, rowTop+2*rowHeight-24)
	if black, all := blackIn(picture, plain), plain.Dx()*plain.Dy(); black == 0 || black > all/3 {
		t.Errorf("a plain row has %d of %d pixels black", black, all)
	}
	// The footer counts the rest; the companion's column stays white for now.
	if blackIn(picture, image.Rect(margin, footerY-30, 200, footerY+8)) == 0 {
		t.Error("the footer does not count the rest")
	}
	if blackIn(picture, image.Rect(leftWidth, 0, Width, Height)) != 0 {
		t.Error("the board drew into the companion's column")
	}
	again := f.Board(rows, 2, "都处理完了", now)
	if !bytes.Equal(Pack(picture), Pack(again)) {
		t.Error("the same board drew two different pictures")
	}
	if bytes.Equal(Pack(picture), Pack(f.Board(rows, 2, "都处理完了", now.Add(time.Minute)))) {
		t.Error("the time of the update is not on the picture")
	}
}

func TestEmptyBoardSaysSo(t *testing.T) {
	f := fonts(t)
	picture := f.Board(nil, 0, "都处理完了", time.Date(2026, 11, 27, 10, 5, 0, 0, time.Local))
	if blackIn(picture, image.Rect(margin, 180, leftWidth-margin, 250)) == 0 {
		t.Error("an empty board is blank")
	}
}

func TestBarPicture(t *testing.T) {
	f := fonts(t)
	bar := f.Bar(time.Date(2026, 11, 27, 22, 30, 0, 0, time.Local))
	if bar.Bounds().Dx() != BarWidth || bar.Bounds().Dy() != BarHeight {
		t.Fatalf("bar is %v", bar.Bounds())
	}
	packed := Pack(bar)
	if len(packed) != BarLen {
		t.Fatalf("bar packs to %d bytes, want %d", len(packed), BarLen)
	}
	// White words on black.
	if white := BarLen*8 - blackIn(bar, bar.Bounds()); white == 0 || white > BarLen*8/3 {
		t.Errorf("the bar has %d white pixels", white)
	}
}

func TestPack(t *testing.T) {
	picture := image.NewGray(image.Rect(0, 0, 16, 2))
	for x := range 16 {
		picture.Pix[x] = 0xff // first row white
	}
	picture.Pix[16] = 0xff // second row: only its first pixel white
	picture.Pix[17] = 0x7f // just below half: black
	got := Pack(picture)
	if want := []byte{0xff, 0xff, 0x80, 0x00}; !bytes.Equal(got, want) {
		t.Errorf("Pack = % x, want % x", got, want)
	}
	if len(Pack(image.NewGray(image.Rect(0, 0, Width, Height)))) != PictureLen {
		t.Error("a full picture does not pack to 48000 bytes")
	}
}

// TestDrawBoardSample writes a sample when asked, to look at by eye:
// EINK_SAMPLE=/tmp/board.pgm go test ./internal/eink -run Sample.
func TestDrawBoardSample(t *testing.T) {
	path := os.Getenv("EINK_SAMPLE")
	if path == "" {
		t.Skip("set EINK_SAMPLE to write a sample picture")
	}
	f := fonts(t)
	picture := f.Board([]Row{
		{Text: "交停车费", When: "已过期 3 天", Urgent: true},
		{Text: "一件医疗的事", When: "明天截止"},
		{Text: "交电费", When: "还有 5 天"},
	}, 2, "都处理完了", time.Now())
	var pgm bytes.Buffer
	pgm.WriteString("P5\n800 480\n255\n")
	pgm.Write(picture.Pix)
	if err := os.WriteFile(path, pgm.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}
