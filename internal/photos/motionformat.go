package photos

import (
	"bytes"
	"encoding/binary"
	"encoding/xml"
	"io"
	"os"
	"strconv"
	"strings"
)

const cameraNS = "http://ns.google.com/photos/1.0/camera/"
const itemNS = "http://ns.google.com/photos/1.0/container/item/"
const containerNS = "http://ns.google.com/photos/1.0/container/"
const maxMotionMetadata = 4 << 20

// motionRange uses declared XMP lengths, never a search for an MP4 signature.
// Unknown vendor formats remain ordinary stills; recognized, broken motion
// metadata fails explicitly so an upload cannot silently discard its clip.
func motionRange(file *os.File, size int64, mediaType string) (int64, int64, error) {
	packets, err := motionXMP(file, size, mediaType)
	if err != nil {
		return 0, 0, err
	}
	for _, packet := range packets {
		length, marked, err := motionLength(packet)
		if err != nil {
			return 0, 0, err
		}
		if !marked {
			continue
		}
		if length < 32 || length >= size {
			return 0, 0, ErrInvalidMotion
		}
		offset := size - length
		var header [12]byte
		if _, err := file.ReadAt(header[:], offset); err != nil || string(header[4:8]) != "ftyp" {
			return 0, 0, ErrInvalidMotion
		}
		box := int64(binary.BigEndian.Uint32(header[:4]))
		if box < 12 || box > length {
			return 0, 0, ErrInvalidMotion
		}
		return offset, length, nil
	}
	return 0, 0, nil
}

func motionXMP(file *os.File, size int64, mediaType string) ([][]byte, error) {
	if mediaType == "image/jpeg" {
		return jpegAPP1(file, size, []byte("http://ns.adobe.com/xap/1.0/\x00"))
	}
	if mediaType != "image/heic" {
		return nil, nil
	}
	// HEIF stores XMP in a MIME item. Bound the metadata window; unsupported
	// placement cannot cause an unbounded allocation or scan of a video payload.
	data := make([]byte, min(size, maxMotionMetadata))
	if _, err := file.ReadAt(data, 0); err != nil && err != io.EOF {
		return nil, err
	}
	for _, tag := range []string{"x:xmpmeta", "xmpmeta"} {
		start := bytes.Index(data, []byte("<"+tag))
		if start < 0 {
			continue
		}
		closeTag := []byte("</" + tag + ">")
		end := bytes.Index(data[start:], closeTag)
		if end < 0 {
			continue
		}
		return [][]byte{data[start : start+end+len(closeTag)]}, nil
	}
	return nil, nil
}

type motionItem struct {
	semantic, mime string
	length         int64
}

func motionLength(data []byte) (int64, bool, error) {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	camera := map[string]string{}
	invalid := func() (int64, bool, error) {
		if camera["MotionPhoto"] == "1" || (camera["MotionPhoto"] == "" && camera["MicroVideo"] == "1") {
			return 0, true, ErrInvalidMotion
		}
		return 0, false, nil
	}
	var items []motionItem
	directoryDepth, depth := 0, 0
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return invalid()
		}
		switch v := token.(type) {
		case xml.StartElement:
			depth++
			if v.Name.Space == containerNS && v.Name.Local == "Directory" {
				directoryDepth = depth
			}
			for _, attr := range v.Attr {
				if attr.Name.Space == cameraNS {
					camera[attr.Name.Local] = attr.Value
				}
			}
			if v.Name.Space == cameraNS {
				switch v.Name.Local {
				case "MotionPhoto", "MicroVideo", "MicroVideoOffset":
					var value string
					if err := decoder.DecodeElement(&value, &v); err != nil {
						return invalid()
					}
					camera[v.Name.Local] = strings.TrimSpace(value)
					depth--
					continue
				}
			}
			if directoryDepth > 0 {
				item := motionItem{}
				for _, attr := range v.Attr {
					if attr.Name.Space != itemNS {
						continue
					}
					switch attr.Name.Local {
					case "Semantic":
						item.semantic = attr.Value
					case "Mime":
						item.mime = attr.Value
					case "Length":
						item.length, _ = strconv.ParseInt(attr.Value, 10, 64)
					}
				}
				if item.semantic != "" {
					items = append(items, item)
				}
			}
		case xml.EndElement:
			if depth == directoryDepth {
				directoryDepth = 0
			}
			depth--
		}
	}
	if flag, ok := camera["MotionPhoto"]; ok {
		if flag != "1" {
			return 0, false, nil
		}
		if len(items) < 2 || items[0].semantic != "Primary" {
			return 0, true, ErrInvalidMotion
		}
		last := items[len(items)-1]
		if last.semantic != "MotionPhoto" || last.mime != "video/mp4" || last.length <= 0 {
			return 0, true, ErrInvalidMotion
		}
		return last.length, true, nil
	}
	if camera["MicroVideo"] == "1" {
		length, err := strconv.ParseInt(camera["MicroVideoOffset"], 10, 64)
		if err != nil || length <= 0 {
			return 0, true, ErrInvalidMotion
		}
		return length, true, nil
	}
	return 0, false, nil
}
