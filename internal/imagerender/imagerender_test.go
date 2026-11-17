package imagerender

import (
	"bytes"
	"context"
	"encoding/binary"
	"image"
	"image/color"
	"testing"
)

// imaging has no release without CVE-2023-36308 (GHSA-q7pp-wcgr-pffx): its
// scanner looks up a paletted image's pixels in the palette unchecked, so a
// pixel past the end of the palette panics. The panic happens in imaging's
// own worker goroutines, where no decode guard (OVERVIEW invariant 18) can
// recover it, and ends the process. A paletted image can reach imaging only
// from a decoder, and every decoder our services register refuses such a
// pixel: x/image's TIFF and BMP decoders, which imaging registers, return an
// error; image/gif returns an error; image/png extends the palette. This test
// keeps the TIFF and BMP decoders to that, with the advisory's own case: a
// colour map shorter than the pixels need.
func TestPixelOutsidePaletteIsUndecodable(t *testing.T) {
	for _, picture := range []struct {
		format string
		encode func(pixels []byte) []byte
	}{
		{"tiff", palettedTIFF},
		{"bmp", palettedBMP},
	} {
		t.Run(picture.format, func(t *testing.T) {
			// The same picture with both pixels in the palette decodes, so the
			// pixel outside it is the only reason the other one does not.
			inside := picture.encode([]byte{1, 0})
			decoded := false
			if err := WithDecoded(context.Background(), bytes.NewReader(inside), func(image.Image) error {
				decoded = true
				return nil
			}); err != nil || !decoded {
				t.Fatalf("the picture with pixels inside its palette: WithDecoded = %v, decoded %v", err, decoded)
			}

			outside := picture.encode([]byte{1, 70})
			config, format, err := image.DecodeConfig(bytes.NewReader(outside))
			if err != nil || format != picture.format {
				t.Fatalf("DecodeConfig = %q, %v; want %s", format, err, picture.format)
			}
			if palette, ok := config.ColorModel.(color.Palette); !ok || len(palette) != 2 {
				t.Fatalf("colour model %T, want a palette of 2", config.ColorModel)
			}
			err = WithDecoded(context.Background(), bytes.NewReader(outside), func(image.Image) error {
				t.Fatal("a pixel outside the palette decoded and would reach imaging")
				return nil
			})
			if err == nil {
				t.Fatal("WithDecoded succeeded, want a decode error")
			}
		})
	}
}

// palettedTIFF and palettedBMP encode a 2x1 picture with a palette of two
// colours and the given two pixels.
func palettedTIFF(pixels []byte) []byte {
	type entry struct {
		tag, kind uint16
		count     uint32
		value     uint32
	}
	const (
		short = 3
		long  = 4
	)
	entries := []entry{
		{256, short, 1, 2},     // ImageWidth
		{257, short, 1, 1},     // ImageLength
		{258, short, 1, 8},     // BitsPerSample
		{259, short, 1, 1},     // Compression: none
		{262, short, 1, 3},     // PhotometricInterpretation: palette
		{273, long, 1, 0},      // StripOffsets, set below
		{277, short, 1, 1},     // SamplesPerPixel
		{278, short, 1, 1},     // RowsPerStrip
		{279, long, 1, 2},      // StripByteCounts
		{320, short, 3 * 2, 0}, // ColorMap: red, green and blue of 2 colours, set below
	}
	colorMap := []uint16{0, 0xffff, 0, 0xffff, 0, 0xffff}
	colorMapOffset := uint32(8 + 2 + 12*len(entries) + 4)
	pixelOffset := colorMapOffset + uint32(2*len(colorMap))
	entries[5].value = pixelOffset
	entries[9].value = colorMapOffset

	le := binary.LittleEndian
	b := le.AppendUint32([]byte("II*\x00"), 8)
	b = le.AppendUint16(b, uint16(len(entries)))
	for _, e := range entries {
		b = le.AppendUint16(b, e.tag)
		b = le.AppendUint16(b, e.kind)
		b = le.AppendUint32(b, e.count)
		b = le.AppendUint32(b, e.value)
	}
	b = le.AppendUint32(b, 0) // no next IFD
	for _, v := range colorMap {
		b = le.AppendUint16(b, v)
	}
	return append(b, pixels...)
}

func palettedBMP(pixels []byte) []byte {
	const (
		headers    = 14 + 40
		colorsUsed = 2
		rowBytes   = 4 // a row is padded to four bytes
	)
	le := binary.LittleEndian
	pixelOffset := uint32(headers + 4*colorsUsed)
	b := le.AppendUint32([]byte("BM"), pixelOffset+rowBytes) // file size
	b = le.AppendUint32(b, 0)                                // reserved
	b = le.AppendUint32(b, pixelOffset)
	b = le.AppendUint32(b, 40) // BITMAPINFOHEADER
	b = le.AppendUint32(b, 2)  // width
	b = le.AppendUint32(b, 1)  // height, bottom-up
	b = le.AppendUint16(b, 1)  // planes
	b = le.AppendUint16(b, 8)  // bits per pixel
	b = le.AppendUint32(b, 0)  // no compression
	b = le.AppendUint32(b, rowBytes)
	b = le.AppendUint32(b, 0) // horizontal resolution
	b = le.AppendUint32(b, 0) // vertical resolution
	b = le.AppendUint32(b, colorsUsed)
	b = le.AppendUint32(b, 0)                      // important colours: all
	b = append(b, 0, 0, 0, 0, 0xff, 0xff, 0xff, 0) // black and white, as BGR0
	return append(append(b, pixels...), 0, 0)      // row padding
}
