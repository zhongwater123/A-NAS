package photos

import (
	"bytes"
	"testing"
)

// The photo service decodes what it stored itself: detected boxes and the
// corners and points of faces, in 65,535ths of the image. Whatever bytes a
// damaged database holds, decoding returns values or an error, never
// panics, and what it accepts encodes back to the same bytes (#223).

func FuzzStoredBoxes(f *testing.F) {
	f.Add(encodeBoxes([]Box{{Class: 1, Score: 0.5, X0: 0.1, Y0: 0.2, X1: 0.3, Y1: 0.4}}))
	f.Add(encodeBoxes(nil))
	f.Add([]byte{})
	f.Add([]byte{boxesVersion, 1, 2, 3})
	f.Add([]byte{boxesVersion + 1})
	f.Fuzz(func(t *testing.T, data []byte) {
		boxes, err := decodeBoxes(data)
		if err != nil {
			return
		}
		if again := encodeBoxes(boxes); !bytes.Equal(again, data) {
			t.Fatalf("decoded %+v from %x, which encodes to %x", boxes, data, again)
		}
	})
}

func FuzzStoredFractions(f *testing.F) {
	f.Add(encodeFractions([]float32{0, 0.25, 0.5, 1}), 4)
	f.Add([]byte{occurrenceVersion}, 0)
	f.Add([]byte{occurrenceVersion, 0xff, 0xff}, 1)
	f.Add([]byte{}, -1)
	f.Fuzz(func(t *testing.T, data []byte, count int) {
		if count < -1 || count > 1<<16 {
			return
		}
		values, err := decodeFractions(data, count)
		if err != nil {
			return
		}
		for _, value := range values {
			if value < 0 || value > 1 {
				t.Fatalf("a fraction %v from %x", value, data)
			}
		}
		if again := encodeFractions(values); !bytes.Equal(again, data) {
			t.Fatalf("decoded %v from %x, which encodes to %x", values, data, again)
		}
	})
}
