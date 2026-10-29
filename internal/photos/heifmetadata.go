package photos

import (
	"errors"
	"io"
	"io/fs"
	"os"

	"github.com/abema/go-mp4"
)

// go-mp4 walks the ISO-BMFF boxes; it does not define the HEIF item property
// boxes (ISO/IEC 23008-12), so they are declared here for its unmarshaller.
var (
	boxTypePitm = mp4.StrToBoxType("pitm")
	boxTypeIprp = mp4.StrToBoxType("iprp")
	boxTypeIpco = mp4.StrToBoxType("ipco")
	boxTypeIpma = mp4.StrToBoxType("ipma")
	boxTypeIspe = mp4.StrToBoxType("ispe")
	boxTypeIrot = mp4.StrToBoxType("irot")
)

func init() {
	mp4.AddBoxDef(&heifPitm{}, 0, 1)
	mp4.AddBoxDef(&heifIprp{})
	mp4.AddBoxDef(&heifIpco{})
	mp4.AddBoxDef(&heifIpma{}, 0, 1)
	mp4.AddBoxDef(&heifIspe{}, 0)
	mp4.AddBoxDef(&heifIrot{})
}

// Version 0 boxes use 16-bit item IDs and version 1 32-bit ones; the field of
// the other version stays zero.
type heifPitm struct {
	mp4.FullBox `mp4:"0,extend"`
	ItemIDV0    uint16 `mp4:"1,size=16,ver=0"`
	ItemIDV1    uint32 `mp4:"2,size=32,nver=0"`
}

func (*heifPitm) GetType() mp4.BoxType { return boxTypePitm }

type heifIprp struct{ mp4.Box }

func (*heifIprp) GetType() mp4.BoxType { return boxTypeIprp }

type heifIpco struct{ mp4.Box }

func (*heifIpco) GetType() mp4.BoxType { return boxTypeIpco }

type heifIpma struct {
	mp4.FullBox `mp4:"0,extend"`
	EntryCount  uint32          `mp4:"1,size=32"`
	Entries     []heifIpmaEntry `mp4:"2,len=dynamic"`
}

func (*heifIpma) GetType() mp4.BoxType { return boxTypeIpma }

func (ipma *heifIpma) GetFieldLength(string, mp4.Context) uint { return uint(ipma.EntryCount) }

type heifIpmaEntry struct {
	mp4.BaseCustomFieldObject
	ItemIDV0         uint16            `mp4:"0,size=16,ver=0"`
	ItemIDV1         uint32            `mp4:"1,size=32,nver=0"`
	AssociationCount uint8             `mp4:"2,size=8"`
	Associations     []heifAssociation `mp4:"3,len=dynamic"`
}

func (entry *heifIpmaEntry) GetFieldLength(string, mp4.Context) uint {
	return uint(entry.AssociationCount)
}

// Flag 1 widens property indices from 7 to 15 bits; the other field stays zero.
type heifAssociation struct {
	Essential bool   `mp4:"0,size=1"`
	Index     uint16 `mp4:"1,size=7,nopt=0x000001"`
	IndexWide uint16 `mp4:"2,size=15,opt=0x000001"`
}

type heifIspe struct {
	mp4.FullBox `mp4:"0,extend"`
	Width       uint32 `mp4:"1,size=32"`
	Height      uint32 `mp4:"2,size=32"`
}

func (*heifIspe) GetType() mp4.BoxType { return boxTypeIspe }

type heifIrot struct {
	mp4.Box
	Reserved uint8 `mp4:"0,size=6"`
	Angle    uint8 `mp4:"1,size=2"`
}

func (*heifIrot) GetType() mp4.BoxType { return boxTypeIrot }

// go-mp4 allocates a few KiB for each box it visits and each ipma entry it
// decodes, so the walk is bounded by box count and by the bytes it reads. Both
// limits sit far above real files and above libheif's default security limits
// (100 children per box, 1000 items), beyond which the FFmpeg Worker could not
// decode the file anyway.
const (
	maxHEIFBoxes    = 4096
	maxHEIFMetadata = 64 << 10
)

// heifProperty is one ipco child at its one-based index. Only the properties
// that set display dimensions are read; a broken one fails only if the
// primary item uses it.
type heifProperty struct {
	box mp4.IBox
	err error
}

// HEIF display dimensions come from the primary item's associated properties,
// not an EXIF thumbnail or one of the tiles of a grid image. All metadata reads
// are bounded and skip mdat/mpvd payloads without reading them.
func heifDimensions(file *os.File) (int, int, error) {
	info, err := file.Stat()
	if err != nil {
		return 0, 0, err
	}
	var primary uint32
	var properties []heifProperty
	var associations []*heifIpma
	boxes, budget := 0, int64(maxHEIFMetadata)
	read := func(h *mp4.ReadHandle) (mp4.IBox, uint64, error) {
		if budget -= int64(h.BoxInfo.Size - h.BoxInfo.HeaderSize); budget < 0 {
			return nil, 0, ErrTooLarge
		}
		return h.ReadPayload()
	}
	_, err = mp4.ReadBoxStructure(io.NewSectionReader(file, 0, info.Size()), func(h *mp4.ReadHandle) (any, error) {
		box := h.BoxInfo
		if box.Size < box.HeaderSize || box.Offset+box.Size > uint64(info.Size()) {
			return nil, ErrUnsupportedType
		}
		if boxes++; boxes > maxHEIFBoxes {
			return nil, ErrTooLarge
		}
		payload := box.Size - box.HeaderSize
		// Containers are expanded only along meta/iprp/ipco, so the depth
		// names the parent.
		switch depth := len(h.Path); {
		case depth == 1 && box.Type == mp4.BoxTypeMeta(),
			depth == 2 && box.Type == boxTypeIprp,
			depth == 3 && box.Type == boxTypeIpco:
			_, err := h.Expand()
			return nil, err
		case depth == 2 && box.Type == boxTypePitm:
			pitm, _, err := read(h)
			if err != nil {
				return nil, err
			}
			primary = uint32(pitm.(*heifPitm).ItemIDV0) + pitm.(*heifPitm).ItemIDV1
		case depth == 3 && box.Type == boxTypeIpma:
			ipma, _, err := read(h)
			if err != nil {
				return nil, err
			}
			associations = append(associations, ipma.(*heifIpma))
		case depth == 4:
			var property heifProperty
			switch box.Type {
			case boxTypeIspe:
				var n uint64
				property.box, n, property.err = read(h)
				if property.err == nil && n != payload {
					property.err = ErrUnsupportedType
				}
			case boxTypeIrot:
				// irot is one byte; any other size is ignored.
				if payload == 1 {
					property.box, _, property.err = read(h)
				}
			}
			if heifFatal(property.err) {
				return nil, property.err
			}
			properties = append(properties, property)
		}
		return nil, nil
	})
	if err != nil {
		if heifFatal(err) {
			return 0, 0, err
		}
		return 0, 0, ErrUnsupportedType
	}
	if primary == 0 || len(associations) == 0 {
		return 0, 0, ErrUnsupportedType
	}
	w, h, rotation := 0, 0, 0
	for _, ipma := range associations {
		for _, entry := range ipma.Entries {
			if uint32(entry.ItemIDV0)+entry.ItemIDV1 != primary {
				continue
			}
			for _, association := range entry.Associations {
				index := int(association.Index + association.IndexWide)
				if index == 0 {
					continue
				}
				if index > len(properties) {
					return 0, 0, ErrUnsupportedType
				}
				property := properties[index-1]
				if property.err != nil {
					return 0, 0, ErrUnsupportedType
				}
				switch box := property.box.(type) {
				case *heifIspe:
					w, h = int(box.Width), int(box.Height)
				case *heifIrot:
					rotation = int(box.Angle)
				}
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

// heifFatal reports the errors that are not about the file's format: the
// metadata budget and I/O.
func heifFatal(err error) bool {
	var pathErr *fs.PathError
	return errors.Is(err, ErrTooLarge) || errors.As(err, &pathErr)
}
