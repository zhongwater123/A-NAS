package photos

import (
	"encoding/binary"
	"io"
	"os"
)

// HEIF display dimensions come from the primary item's associated properties,
// not an EXIF thumbnail or one of the tiles of a grid image. All metadata reads
// are bounded and skip mdat/mpvd payloads without allocating them.
func heifDimensions(file *os.File) (int, int, error) {
	info, err := file.Stat()
	if err != nil {
		return 0, 0, err
	}
	var primary uint32
	var properties [][]byte
	var associations []byte
	budget := int64(maxMotionMetadata)
	var walk func(int64, int64, int) error
	walk = func(start, end int64, depth int) error {
		if depth > 4 {
			return ErrUnsupportedType
		}
		for pos := start; pos+8 <= end; {
			var header [16]byte
			if _, err := file.ReadAt(header[:8], pos); err != nil {
				return err
			}
			size := int64(binary.BigEndian.Uint32(header[:4]))
			head := int64(8)
			if size == 1 {
				if _, err := file.ReadAt(header[8:], pos+8); err != nil {
					return err
				}
				raw := binary.BigEndian.Uint64(header[8:])
				if raw > uint64(end-pos) {
					return ErrUnsupportedType
				}
				size = int64(raw)
				head = 16
			}
			if size == 0 {
				size = end - pos
			}
			if size < head || size > end-pos {
				return ErrUnsupportedType
			}
			budget -= head
			if budget < 0 || len(properties) > 32767 {
				return ErrTooLarge
			}
			kind := string(header[4:8])
			body := pos + head
			switch kind {
			case "meta":
				if err := walk(body+4, pos+size, depth+1); err != nil {
					return err
				}
			case "iprp", "ipco":
				if err := walk(body, pos+size, depth+1); err != nil {
					return err
				}
			case "pitm", "ipma", "ispe", "irot", "imir", "hvcC", "pixi", "colr", "auxC", "clap":
				count := size - head
				if count > budget {
					return ErrTooLarge
				}
				budget -= count
				data := make([]byte, count)
				if _, err := file.ReadAt(data, body); err != nil && err != io.EOF {
					return err
				}
				if kind == "pitm" {
					if len(data) < 6 {
						return ErrUnsupportedType
					}
					if data[0] == 0 {
						primary = uint32(binary.BigEndian.Uint16(data[4:]))
					} else {
						if len(data) < 8 {
							return ErrUnsupportedType
						}
						primary = binary.BigEndian.Uint32(data[4:])
					}
				}
				if kind == "ipma" {
					associations = data
				}
				// Properties retain their one-based indices, including ignored types.
				if depth == 3 {
					properties = append(properties, append([]byte(kind), data...))
				}
			default:
				if depth == 3 {
					properties = append(properties, nil)
				}
			}
			pos += size
		}
		return nil
	}
	if err := walk(0, info.Size(), 0); err != nil {
		return 0, 0, err
	}
	if primary == 0 || len(associations) < 8 {
		return 0, 0, ErrUnsupportedType
	}
	data := associations
	version := data[0]
	wide := data[3]&1 != 0
	entries := binary.BigEndian.Uint32(data[4:])
	pos := 8
	var indices []int
	for n := uint32(0); n < entries; n++ {
		width := 2
		if version >= 1 {
			width = 4
		}
		if pos+width+1 > len(data) {
			return 0, 0, ErrUnsupportedType
		}
		id := uint32(binary.BigEndian.Uint16(data[pos:]))
		if width == 4 {
			id = binary.BigEndian.Uint32(data[pos:])
		}
		pos += width
		count := int(data[pos])
		pos++
		for range count {
			width = 1
			if wide {
				width = 2
			}
			if pos+width > len(data) {
				return 0, 0, ErrUnsupportedType
			}
			index := int(data[pos] & 0x7f)
			if wide {
				index = int(binary.BigEndian.Uint16(data[pos:]) & 0x7fff)
			}
			pos += width
			if id == primary {
				indices = append(indices, index)
			}
		}
	}
	w, h, rotation := 0, 0, 0
	for _, index := range indices {
		if index == 0 {
			continue
		}
		if index > len(properties) {
			return 0, 0, ErrUnsupportedType
		}
		p := properties[index-1]
		if len(p) < 4 {
			continue
		}
		switch string(p[:4]) {
		case "ispe":
			if len(p) != 16 {
				return 0, 0, ErrUnsupportedType
			}
			w = int(binary.BigEndian.Uint32(p[8:12]))
			h = int(binary.BigEndian.Uint32(p[12:16]))
		case "irot":
			if len(p) == 5 {
				rotation = int(p[4] & 3)
			}
		}
	}
	if w <= 0 || h <= 0 {
		return 0, 0, ErrUnsupportedType
	}
	if int64(w) > maxImagePixels/int64(h) {
		return 0, 0, ErrTooLarge
	}
	if rotation%2 == 1 {
		w, h = h, w
	}
	return w, h, nil
}
