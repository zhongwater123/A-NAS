package photos_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"image/jpeg"
	"math"
	"strings"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/photos"
)

// gpsJPEG returns a small JPEG whose EXIF names the camera and records a GPS
// position: degrees, minutes and seconds with their N/S and E/W references,
// and an altitude in metres.
func gpsJPEG(t *testing.T, lat [3]uint32, latRef string, lon [3]uint32, lonRef string, altitudeCM uint32) []byte {
	t.Helper()
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 16, 12)), nil); err != nil {
		t.Fatalf("encode JPEG: %v", err)
	}
	le := binary.LittleEndian
	var tiff bytes.Buffer
	write := func(values ...any) {
		for _, value := range values {
			_ = binary.Write(&tiff, le, value)
		}
	}
	entry := func(tag, kind uint16, count uint32, value [4]byte) { write(tag, kind, count, value) }
	offset := func(n uint32) (b [4]byte) { le.PutUint32(b[:], n); return b }
	const make_, model = "Apple\x00", "iPhone 15 Pro\x00"
	const ifd0 = 8
	strings0 := uint32(ifd0 + 2 + 3*12 + 4)
	gpsIFD := strings0 + uint32(len(make_)+len(model))
	if gpsIFD%2 == 1 {
		gpsIFD++
	}
	rationals := gpsIFD + 2 + 7*12 + 4
	write([]byte("II"), uint16(42), uint32(ifd0))
	write(uint16(3))
	entry(0x010f, 2, uint32(len(make_)), offset(strings0))
	entry(0x0110, 2, uint32(len(model)), offset(strings0+uint32(len(make_))))
	entry(0x8825, 4, 1, offset(gpsIFD))
	write(uint32(0))
	tiff.WriteString(make_ + model)
	for uint32(tiff.Len()) < gpsIFD {
		tiff.WriteByte(0)
	}
	write(uint16(7))
	entry(0x0000, 1, 4, [4]byte{2, 3, 0, 0})
	entry(0x0001, 2, 2, [4]byte{latRef[0]})
	entry(0x0002, 5, 3, offset(rationals))
	entry(0x0003, 2, 2, [4]byte{lonRef[0]})
	entry(0x0004, 5, 3, offset(rationals+24))
	entry(0x0005, 1, 1, [4]byte{0})
	entry(0x0006, 5, 1, offset(rationals+48))
	write(uint32(0))
	for _, part := range lat {
		write(part, uint32(1))
	}
	for _, part := range lon {
		write(part, uint32(1))
	}
	write(altitudeCM, uint32(100))

	app1 := append([]byte("Exif\x00\x00"), tiff.Bytes()...)
	var out bytes.Buffer
	out.Write(encoded.Bytes()[:2])
	out.Write([]byte{0xff, 0xe1})
	_ = binary.Write(&out, binary.BigEndian, uint16(len(app1)+2))
	out.Write(app1)
	out.Write(encoded.Bytes()[2:])
	return out.Bytes()
}

// A member, and their AI assistant, read everything a photo's original
// records about it, where it was taken among it.
func TestAPhotosMetadataComesFromItsOriginal(t *testing.T) {
	ctx := context.Background()
	service, _, _ := newService(t)
	alicePrivate, _ := libraries(t, service, alice)
	bobPrivate, _ := libraries(t, service, bob)
	// 36°4'1"N 120°22'59"E, 12.5 m: Qingdao; and 33°52'S 151°13'E: Sydney.
	qingdao := importPhoto(t, service, alice, alicePrivate.ID, "", "IMG_2031.JPG",
		gpsJPEG(t, [3]uint32{36, 4, 1}, "N", [3]uint32{120, 22, 59}, "E", 1250))
	sydney := importPhoto(t, service, alice, alicePrivate.ID, "", "IMG_2032.JPG",
		gpsJPEG(t, [3]uint32{33, 52, 0}, "S", [3]uint32{151, 13, 0}, "E", 0))
	plain := importPhoto(t, service, alice, alicePrivate.ID, "", "plain.png", pngBytes(t, 9))
	bobs := importPhoto(t, service, bob, bobPrivate.ID, "", "bob.jpg",
		gpsJPEG(t, [3]uint32{31, 14, 0}, "N", [3]uint32{121, 28, 0}, "E", 0))
	assistant := photos.Principal{UserID: alice.UserID, Username: "alice", Assistant: true}

	for _, p := range []photos.Principal{alice, assistant} {
		metadata, err := service.Metadata(ctx, p, qingdao.ID)
		if err != nil {
			t.Fatalf("Metadata() error = %v", err)
		}
		where := metadata.Location
		if where == nil || math.Abs(where.Latitude-36.0669) > 0.001 || math.Abs(where.Longitude-120.3831) > 0.001 ||
			where.Altitude == nil || math.Abs(*where.Altitude-12.5) > 0.01 {
			t.Fatalf("location = %+v, want Qingdao at 12.5 m", where)
		}
		if !strings.Contains(string(metadata.EXIF), "iPhone 15 Pro") || !strings.Contains(string(metadata.EXIF), `"gps"`) {
			t.Fatalf("exif = %s, want the camera and the GPS tags", metadata.EXIF)
		}
	}
	south, err := service.Metadata(ctx, alice, sydney.ID)
	if err != nil {
		t.Fatalf("Metadata() error = %v", err)
	}
	if south.Location == nil || south.Location.Latitude > -33.8 || south.Location.Longitude < 151.2 || south.Location.Altitude != nil {
		t.Fatalf("location = %+v, want Sydney, south of the equator, without an altitude", south.Location)
	}
	none, err := service.Metadata(ctx, assistant, plain.ID)
	if err != nil {
		t.Fatalf("Metadata() of a photo without EXIF error = %v", err)
	}
	if none.Location != nil {
		t.Fatalf("location of a photo without EXIF = %+v", none.Location)
	}
	if _, err := service.Metadata(ctx, assistant, bobs.ID); !errors.Is(err, photos.ErrNotFound) {
		t.Fatalf("Metadata() of bob's photo error = %v, want ErrNotFound", err)
	}
}
