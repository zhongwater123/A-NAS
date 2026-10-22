package taskmanager

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/xml"
	"image"
	_ "image/jpeg"
	"image/png"
	"io"
	"strings"
	"unicode"

	"github.com/disintegration/imaging"
	_ "golang.org/x/image/webp"
)

const MaxIcons = 64
const MaxIconDataURL = 24576
const MaxIconBytes = 256 << 10

// NormalizeIcon accepts installed artwork, never arbitrary file content. Raster
// inputs become small PNGs; SVGs must be self-contained static image documents.
// An icon the decoder panics on is dropped, like one it rejects: the Host
// Agent reads the same file again on every refresh.
func NormalizeIcon(data []byte) (id, dataURL string) {
	defer func() {
		if recover() != nil {
			id, dataURL = "", ""
		}
	}()
	if len(data) == 0 || len(data) > MaxIconBytes {
		return "", ""
	}
	mime := "image/png"
	if bytes.HasPrefix(bytes.TrimSpace(data), []byte("<")) {
		if !staticSVG(data) {
			return "", ""
		}
		mime = "image/svg+xml"
	} else {
		config, _, err := image.DecodeConfig(bytes.NewReader(data))
		if err != nil || config.Width <= 0 || config.Height <= 0 || config.Width > 2048 || config.Height > 2048 || config.Width*config.Height > 1024*1024 {
			return "", ""
		}
		original, _, err := image.Decode(bytes.NewReader(data))
		if err != nil {
			return "", ""
		}
		var out bytes.Buffer
		if err := png.Encode(&out, imaging.Fit(original, 64, 64, imaging.Lanczos)); err != nil {
			return "", ""
		}
		data = out.Bytes()
	}
	dataURL = "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data)
	if len(dataURL) > MaxIconDataURL {
		return "", ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), dataURL
}

func (s TaskSnapshot) iconsValid() bool {
	if len(s.Icons) > MaxIcons {
		return false
	}
	total := 0
	for id, source := range s.Icons {
		total += len(source)
		if total > 1<<20 || !validIcon(id, source) {
			return false
		}
	}
	for _, group := range []TaskGroup{s.Services, s.Processes} {
		for _, row := range group.Items {
			if row.IconID != "" {
				if _, ok := s.Icons[row.IconID]; !ok {
					return false
				}
			}
		}
	}
	return true
}
func validIcon(id, source string) bool {
	if len(source) > MaxIconDataURL || len(id) != 64 {
		return false
	}
	header, encoded, ok := strings.Cut(source, ",")
	if !ok || (header != "data:image/png;base64" && header != "data:image/svg+xml;base64") {
		return false
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return false
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != id {
		return false
	}
	if header == "data:image/svg+xml;base64" {
		return staticSVG(data)
	}
	config, err := png.DecodeConfig(bytes.NewReader(data))
	return err == nil && config.Width > 0 && config.Height > 0 && config.Width <= 64 && config.Height <= 64
}

// SVG images are embedded only via img. Reject active content and any external
// resource so rendering an installed icon never causes a browser network fetch.
func staticSVG(data []byte) bool {
	if len(data) > 18000 {
		return false
	}
	tags := map[string]bool{}
	for _, name := range strings.Fields("svg g path rect circle ellipse line polyline polygon defs linearGradient radialGradient stop clipPath mask title desc use symbol") {
		tags[name] = true
	}
	attrs := map[string]bool{}
	for _, name := range strings.Fields("xmlns version id class width height viewBox preserveAspectRatio x y x1 x2 y1 y2 cx cy r rx ry d points fill fill-rule fill-opacity stroke stroke-width stroke-linecap stroke-linejoin stroke-miterlimit stroke-dasharray stroke-dashoffset stroke-opacity opacity transform gradientUnits gradientTransform spreadMethod offset stop-color stop-opacity clip-path clip-rule mask maskUnits maskContentUnits filter href style role aria-label aria-labelledby focusable") {
		attrs[name] = true
	}
	decoder := xml.NewDecoder(bytes.NewReader(data))
	depth, elements := 0, 0
	closed := false
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return closed && depth == 0 && elements > 0
		}
		if err != nil {
			return false
		}
		switch t := token.(type) {
		case xml.StartElement:
			if closed || !tags[t.Name.Local] || (t.Name.Space != "" && t.Name.Space != "http://www.w3.org/2000/svg") {
				return false
			}
			if depth == 0 && t.Name.Local != "svg" {
				return false
			}
			depth++
			elements++
			if depth > 32 || elements > 1000 {
				return false
			}
			for _, a := range t.Attr {
				if a.Name.Space == "xmlns" {
					if a.Name.Local != "xlink" || a.Value != "http://www.w3.org/1999/xlink" {
						return false
					}
					continue
				}
				if !attrs[a.Name.Local] || (a.Name.Space != "" && a.Name.Space != "http://www.w3.org/1999/xlink") {
					return false
				}
				if a.Name.Local == "xmlns" {
					if a.Value != "http://www.w3.org/2000/svg" {
						return false
					}
					continue
				}
				if a.Name.Local == "href" {
					if !fragment(a.Value) {
						return false
					}
					continue
				}
				if !staticSVGValue(a.Value) {
					return false
				}
			}
		case xml.EndElement:
			depth--
			if depth == 0 {
				closed = true
			}
		case xml.Directive:
			return false
		case xml.ProcInst:
			if t.Target != "xml" || elements != 0 {
				return false
			}
		case xml.CharData:
			if depth == 0 && strings.TrimSpace(string(t)) != "" {
				return false
			}
		}
	}
}
func fragment(value string) bool {
	if len(value) < 2 || value[0] != '#' {
		return false
	}
	for _, r := range value[1:] {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' && r != '-' && r != '.' {
			return false
		}
	}
	return true
}
func staticSVGValue(value string) bool {
	lower := strings.ToLower(value)
	for _, forbidden := range []string{"\\", "@", "expression", "javascript", "data:", "http:", "https:", "//", "/*", "&"} {
		if strings.Contains(lower, forbidden) {
			return false
		}
	}
	for {
		i := strings.Index(lower, "url")
		if i < 0 {
			return true
		}
		rest := strings.TrimSpace(lower[i+3:])
		if !strings.HasPrefix(rest, "(") {
			return false
		}
		end := strings.IndexByte(rest, ')')
		if end < 0 {
			return false
		}
		if !fragment(strings.Trim(strings.TrimSpace(rest[1:end]), "\"'")) {
			return false
		}
		lower = rest[end+1:]
	}
}
