package photos

import (
	"bytes"
	"encoding/binary"
	"io"
	"os"
	"strings"

	"github.com/evanoberholster/imagemeta/meta"
	"github.com/evanoberholster/imagemeta/meta/isobmff"
)

// Apple uses a 14-byte maker-note preamble and offsets relative to that
// preamble. imagemeta v1.1.0 instead skips 26 bytes, losing ContentIdentifier.
// Keep this narrow reader until that dependency's regression is fixed.
func appleContentID(file *os.File) (id string) {
	defer func() {
		if recover() != nil {
			id = ""
		}
	}()
	var header [32]byte
	n, _ := file.ReadAt(header[:], 0)
	if sniffMediaType(header[:n]) == "image/jpeg" {
		info, err := file.Stat()
		if err != nil {
			return ""
		}
		packets, _ := jpegAPP1(file, info.Size(), []byte("Exif\x00\x00"))
		for _, packet := range packets {
			if id := appleTIFFID(packet); id != "" {
				return id
			}
		}
		return ""
	}
	if sniffMediaType(header[:n]) != "image/heic" {
		return ""
	}
	_, _ = file.Seek(0, io.SeekStart)
	reader := isobmff.NewReader(file, func(r io.Reader, header meta.ExifHeader) error {
		data, err := io.ReadAll(io.LimitReader(r, maxMotionMetadata+1))
		if err != nil {
			return err
		}
		if len(data) > maxMotionMetadata {
			return ErrTooLarge
		}
		// The BMFF callback starts immediately after the eight-byte TIFF header.
		if header.BigTIFF {
			return ErrUnsupportedType
		}
		prefix := make([]byte, 8)
		header.ByteOrder.PutUint16(prefix[2:], 42)
		if prefix[2] == 0 {
			copy(prefix, "MM")
		} else {
			copy(prefix, "II")
		}
		header.ByteOrder.PutUint32(prefix[4:], header.FirstIfdOffset)
		id = appleTIFFID(append(prefix, data...))
		return nil
	}, nil, nil)
	defer reader.Close()
	if reader.ReadFTYP() != nil {
		return ""
	}
	_ = reader.ReadMetadataUntilEOF()
	return id
}

func tiffOrder(data []byte) binary.ByteOrder {
	if len(data) < 2 {
		return nil
	}
	switch string(data[:2]) {
	case "MM":
		return binary.BigEndian
	case "II":
		return binary.LittleEndian
	}
	return nil
}
func ifdEntry(data []byte, offset uint32, order binary.ByteOrder, tag uint16) []byte {
	if order == nil || uint64(offset)+2 > uint64(len(data)) {
		return nil
	}
	count := uint64(order.Uint16(data[offset:]))
	start := uint64(offset) + 2
	if start+count*12 > uint64(len(data)) {
		return nil
	}
	for i := uint64(0); i < count; i++ {
		entry := data[start+i*12 : start+(i+1)*12]
		if order.Uint16(entry) == tag {
			return entry
		}
	}
	return nil
}
func tiffBytes(data, entry []byte, order binary.ByteOrder) []byte {
	if len(entry) != 12 {
		return nil
	}
	kind := order.Uint16(entry[2:])
	if kind != 2 && kind != 7 {
		return nil
	}
	length := uint64(order.Uint32(entry[4:]))
	if length <= 4 {
		return entry[8 : 8+length]
	}
	offset := uint64(order.Uint32(entry[8:]))
	if offset+length > uint64(len(data)) {
		return nil
	}
	return data[offset : offset+length]
}
func appleTIFFID(data []byte) string {
	order := tiffOrder(data)
	if order == nil || len(data) < 8 || order.Uint16(data[2:]) != 42 {
		return ""
	}
	exif := ifdEntry(data, order.Uint32(data[4:]), order, 0x8769)
	if len(exif) != 12 || order.Uint16(exif[2:]) != 4 || order.Uint32(exif[4:]) != 1 {
		return ""
	}
	maker := ifdEntry(data, order.Uint32(exif[8:]), order, 0x927c)
	note := tiffBytes(data, maker, order)
	if len(note) < 16 || !bytes.HasPrefix(note, []byte("Apple iOS\x00")) {
		return ""
	}
	order = tiffOrder(note[12:14])
	if order == nil {
		return ""
	}
	identifier := ifdEntry(note, 14, order, 0x0011)
	value := tiffBytes(note, identifier, order)
	if len(value) > 128 {
		return ""
	}
	id := strings.TrimRight(string(value), "\x00")
	for _, char := range id {
		if char < 33 || char > 126 {
			return ""
		}
	}
	return id
}

// Read only JPEG APP1 segments before scan data, bounded independently of image
// and video size. EXIF and XMP callers choose their own namespace prefix.
func jpegAPP1(file *os.File, size int64, prefix []byte) ([][]byte, error) {
	var packets [][]byte
	for offset := int64(2); offset+4 <= size && offset < maxMotionMetadata; {
		var header [4]byte
		if _, err := file.ReadAt(header[:], offset); err != nil {
			return nil, nil
		}
		if header[0] != 0xff {
			return nil, nil
		}
		if header[1] == 0xda || header[1] == 0xd9 {
			break
		}
		if header[1] == 0xff {
			offset++
			continue
		}
		length := int64(binary.BigEndian.Uint16(header[2:]))
		if length < 2 || offset+2+length > size {
			return nil, nil
		}
		if header[1] == 0xe1 {
			data := make([]byte, length-2)
			if _, err := file.ReadAt(data, offset+4); err != nil {
				return nil, err
			}
			if bytes.HasPrefix(data, prefix) {
				packets = append(packets, data[len(prefix):])
			}
		}
		offset += 2 + length
	}
	return packets, nil
}
