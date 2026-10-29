package photos_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/ffmpegworker"
	"github.com/zhongwater123/A-NAS/internal/photos"
)

type motionDecoder struct{ probe ffmpegworker.Probe }

func (m motionDecoder) Probe(context.Context, *os.File) (ffmpegworker.Probe, error) {
	return m.probe, nil
}
func (m motionDecoder) Image(context.Context, *os.File, int) ([]byte, error) {
	return nil, ffmpegworker.ErrUnreadable
}
func (m motionDecoder) Frame(context.Context, *os.File, float64, int) ([]byte, error) {
	return nil, ffmpegworker.ErrUnreadable
}
func (m motionDecoder) Stream(context.Context, *os.File, ffmpegworker.StreamRequest) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader([]byte("compatible movie"))), nil
}
func motionProbe() ffmpegworker.Probe {
	return ffmpegworker.Probe{Format: ffmpegworker.ProbeFormat{Name: "mov,mp4", Duration: 3}, Streams: []ffmpegworker.ProbeStream{{Type: "video", Width: 320, Height: 240}}}
}
func movieBytes() []byte {
	b := make([]byte, 64)
	binary.BigEndian.PutUint32(b, 24)
	copy(b[4:], "ftypqt  ")
	return b
}
func motionService(t *testing.T, probe ffmpegworker.Probe) (*photos.Service, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "photos")
	s, err := photos.Open(root, photos.Options{Media: motionDecoder{probe}, DisableCapacityReserve: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, root
}
func TestLivePhotoLifecycleAndAIQueue(t *testing.T) {
	ctx := context.Background()
	s, root := motionService(t, motionProbe())
	private, shared := libraries(t, s, alice)
	still, clip := jpegBytes(t), movieBytes()
	asset, err := s.Import(ctx, alice, photos.ImportRequest{LibraryID: private.ID, Name: "IMG_1.JPG", Content: bytes.NewReader(still), MotionName: "IMG_1.MOV", MotionContent: bytes.NewReader(clip)})
	if err != nil {
		t.Fatal(err)
	}
	if asset.Motion == nil || asset.Motion.Kind != "live-photo" {
		t.Fatalf("missing motion: %+v", asset)
	}
	if _, err := s.OpenMotion(ctx, bob, asset.ID); !errors.Is(err, photos.ErrNotFound) {
		t.Fatalf("private motion leaked: %v", err)
	}
	copy, err := s.Copy(ctx, alice, asset.ID, shared.ID, "")
	if err != nil || copy.Motion == nil {
		t.Fatalf("copy: %+v %v", copy, err)
	}
	if _, err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ProcessMediaJob(ctx); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite3", filepath.Join(root, "catalog.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var videoJobs int
	if err := db.QueryRow(`SELECT COUNT(*) FROM jobs j JOIN objects o ON o.id=j.object_id WHERE o.media_type LIKE 'video/%'`).Scan(&videoJobs); err != nil || videoJobs != 0 {
		t.Fatalf("video reached AI: %d %v", videoJobs, err)
	}
	if _, err := s.Trash(ctx, alice, asset.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Purge(ctx, alice, asset.ID); err != nil {
		t.Fatal(err)
	}
	content, err := s.OpenMotion(ctx, bob, copy.ID)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(content.Reader)
	_ = content.Reader.Close()
	if !bytes.Equal(got, clip) {
		t.Fatal("copy lost original movie")
	}
	if _, err := s.Trash(ctx, alice, copy.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Purge(ctx, alice, copy.ID); err != nil {
		t.Fatal(err)
	}
	var objects int
	if err := db.QueryRow("SELECT COUNT(*) FROM objects").Scan(&objects); err != nil || objects != 0 {
		t.Fatalf("leaked originals: %d %v", objects, err)
	}
}
func TestInvalidLivePhotoLeavesNoAsset(t *testing.T) {
	for _, mode := range []string{"mismatch", "long", "audio-only", "name"} {
		t.Run(mode, func(t *testing.T) {
			probe := motionProbe()
			name := "a.mov"
			switch mode {
			case "mismatch":
				probe.Format.Tags = map[string]string{"com.apple.quicktime.content.identifier": "other"}
			case "long":
				probe.Format.Duration = 31
			case "audio-only":
				probe.Streams = nil
			case "name":
				name = "b.mov"
			}
			s, root := motionService(t, probe)
			private, _ := libraries(t, s, alice)
			_, err := s.Import(context.Background(), alice, photos.ImportRequest{LibraryID: private.ID, Name: "a.jpg", Content: bytes.NewReader(jpegBytes(t)), MotionName: name, MotionContent: bytes.NewReader(movieBytes())})
			if !errors.Is(err, photos.ErrInvalidMotion) {
				t.Fatal(err)
			}
			page, err := s.Timeline(context.Background(), alice, private.ID, "", 10)
			if err != nil || len(page.Assets) != 0 {
				t.Fatalf("partial asset: %+v %v", page, err)
			}
			files, _ := os.ReadDir(filepath.Join(root, "staging"))
			if len(files) != 0 {
				t.Fatal("leaked staging")
			}
		})
	}
}
func androidJPEG(t *testing.T, xmp string, clip []byte) []byte {
	photo := jpegBytes(t)
	payload := append([]byte("http://ns.adobe.com/xap/1.0/\x00"), []byte(xmp)...)
	segment := []byte{0xff, 0xe1, 0, 0}
	binary.BigEndian.PutUint16(segment[2:], uint16(len(payload)+2))
	out := append([]byte{}, photo[:2]...)
	out = append(out, segment...)
	out = append(out, payload...)
	out = append(out, photo[2:]...)
	return append(out, clip...)
}
func TestAndroidMotionPhotoFormats(t *testing.T) {
	clip := movieBytes()
	for _, modern := range []bool{false, true} {
		t.Run(fmt.Sprint(modern), func(t *testing.T) {
			xmp := fmt.Sprintf(`<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#"><rdf:Description xmlns:G="http://ns.google.com/photos/1.0/camera/" G:MicroVideo="1" G:MicroVideoOffset="%d" /></rdf:RDF></x:xmpmeta>`, len(clip))
			if modern {
				xmp = fmt.Sprintf(`<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#"><rdf:Description xmlns:G="http://ns.google.com/photos/1.0/camera/" xmlns:C="http://ns.google.com/photos/1.0/container/" xmlns:I="http://ns.google.com/photos/1.0/container/item/" G:MotionPhoto="1"><C:Directory><rdf:Seq><rdf:li><C:Item I:Semantic="Primary" I:Mime="image/jpeg" /></rdf:li><rdf:li><C:Item I:Semantic="MotionPhoto" I:Mime="video/mp4" I:Length="%d" /></rdf:li></rdf:Seq></C:Directory></rdf:Description></rdf:RDF></x:xmpmeta>`, len(clip))
			}
			data := androidJPEG(t, xmp, clip)
			s, _ := motionService(t, motionProbe())
			private, _ := libraries(t, s, alice)
			asset, err := s.Import(context.Background(), alice, photos.ImportRequest{LibraryID: private.ID, Name: "android.jpg", Content: bytes.NewReader(data)})
			if err != nil || asset.Motion == nil || asset.Motion.Kind != "motion-photo" {
				t.Fatalf("import: %+v %v", asset, err)
			}
			original, err := s.Open(context.Background(), alice, asset.ID)
			if err != nil {
				t.Fatal(err)
			}
			got, _ := io.ReadAll(original.Reader)
			_ = original.Reader.Close()
			if !bytes.Equal(data, got) {
				t.Fatal("Android original changed")
			}
			movie, err := s.OpenMotion(context.Background(), alice, asset.ID)
			if err != nil {
				t.Fatal(err)
			}
			got, _ = io.ReadAll(movie.Reader)
			_ = movie.Reader.Close()
			if !bytes.Equal(clip, got) {
				t.Fatal("wrong video extraction")
			}
		})
	}
}

// Minimal, big-endian EXIF with Apple's maker-note content identifier. The
// inline ASCII value exercises the real metadata parser without a personal file.
func appleJPEG(t testing.TB, id string) []byte {
	if len(id) < 1 || len(id) > 100 {
		t.Fatal("invalid fixture identifier")
	}
	length := 94
	if len(id)+1 > 4 {
		length += len(id) + 1
	}
	exif := make([]byte, length)
	copy(exif, "MM")
	binary.BigEndian.PutUint16(exif[2:], 42)
	binary.BigEndian.PutUint32(exif[4:], 8)
	entry := func(at int, tag, kind uint16, count, value uint32) {
		binary.BigEndian.PutUint16(exif[at:], tag)
		binary.BigEndian.PutUint16(exif[at+2:], kind)
		binary.BigEndian.PutUint32(exif[at+4:], count)
		binary.BigEndian.PutUint32(exif[at+8:], value)
	}
	binary.BigEndian.PutUint16(exif[8:], 2)
	entry(10, 0x010f, 2, 6, 38)
	entry(22, 0x8769, 4, 1, 44)
	copy(exif[38:], "Apple\x00")
	binary.BigEndian.PutUint16(exif[44:], 1)
	entry(46, 0x927c, 7, uint32(length-62), 62)
	copy(exif[62:], "Apple iOS\x00\x00\x01MM")
	binary.BigEndian.PutUint16(exif[76:], 1)
	entry(78, 0x0011, 2, uint32(len(id)+1), 0)
	if len(id)+1 > 4 {
		binary.BigEndian.PutUint32(exif[86:], 32)
		copy(exif[94:], id)
	} else {
		copy(exif[86:], id)
	}
	payload := append([]byte("Exif\x00\x00"), exif...)
	segment := []byte{0xff, 0xe1, 0, 0}
	binary.BigEndian.PutUint16(segment[2:], uint16(len(payload)+2))
	original := jpegBytes(t)
	result := append([]byte{}, original[:2]...)
	result = append(result, segment...)
	result = append(result, payload...)
	return append(result, original[2:]...)
}
func FuzzAppleJPEGContentID(f *testing.F) {
	for _, id := range []string{"abc", "01234567-89AB-CDEF-0123-456789ABCDEF"} {
		f.Add(appleJPEG(f, id))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		path := filepath.Join(t.TempDir(), "live.jpg")
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		file, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = file.Close() }()
		packets, _ := photos.JPEGAPP1(file, int64(len(data)), []byte("Exif\x00\x00"))
		for _, packet := range packets {
			if id := photos.AppleTIFFID(packet); len(id) > 128 || strings.ContainsFunc(id, func(r rune) bool { return r < 33 || r > 126 }) {
				t.Fatalf("content identifier %q", id)
			}
		}
	})
}
func TestAppleContentIdentifiersPairAndRejectMismatch(t *testing.T) {
	for _, id := range []string{"abc", "bad", "01234567-89AB-CDEF-0123-456789ABCDEF"} {
		t.Run(id, func(t *testing.T) {
			probe := motionProbe()
			probe.Format.Tags = map[string]string{"com.apple.quicktime.content.identifier": id}
			s, _ := motionService(t, probe)
			private, _ := libraries(t, s, alice)
			stillID := "abc"
			if len(id) > 3 {
				stillID = id
			}
			asset, err := s.Import(context.Background(), alice, photos.ImportRequest{LibraryID: private.ID, Name: "live.jpg", Content: bytes.NewReader(appleJPEG(t, stillID)), MotionName: "live.mov", MotionContent: bytes.NewReader(movieBytes())})
			if id != "bad" {
				if err != nil || asset.Motion == nil {
					t.Fatalf("matching Apple identifiers refused: %+v %v", asset, err)
				}
			} else if !errors.Is(err, photos.ErrInvalidMotion) {
				t.Fatalf("mismatched Apple identifiers accepted: %v", err)
			}
		})
	}
}

func TestAppleIdentifierInHEIFExifItem(t *testing.T) {
	identifier := "01234567-89AB-CDEF-0123-456789ABCDEF"
	jpeg := appleJPEG(t, identifier)
	tiff := jpeg[12 : 4+int(binary.BigEndian.Uint16(jpeg[4:6]))]
	box := func(kind string, parts ...[]byte) []byte {
		payload := bytes.Join(parts, nil)
		out := make([]byte, 8)
		binary.BigEndian.PutUint32(out, uint32(8+len(payload)))
		copy(out[4:], kind)
		return append(out, payload...)
	}
	dimensions := make([]byte, 12)
	binary.BigEndian.PutUint32(dimensions[4:], 16)
	binary.BigEndian.PutUint32(dimensions[8:], 16)
	metadata := box("meta", make([]byte, 4), box("pitm", []byte{0, 0, 0, 0, 0, 1}), box("iprp", box("ipco", box("ispe", dimensions)), box("ipma", []byte{0, 0, 0, 0, 0, 0, 0, 1, 0, 1, 1, 1})))
	heic := box("ftyp", []byte("heic\x00\x00\x00\x00heic"))
	heic = append(heic, metadata...)
	heic = append(heic, box("Exif", make([]byte, 4), tiff)...)

	probe := motionProbe()
	probe.Format.Tags = map[string]string{"com.apple.quicktime.content.identifier": identifier}
	s, _ := motionService(t, probe)
	private, _ := libraries(t, s, alice)
	asset, err := s.Import(context.Background(), alice, photos.ImportRequest{LibraryID: private.ID, Name: "live.heic", Content: bytes.NewReader(heic), MotionName: "live.mov", MotionContent: bytes.NewReader(movieBytes())})
	if err != nil || asset.Motion == nil {
		t.Fatalf("HEIF identifier: %+v %v", asset, err)
	}
}
