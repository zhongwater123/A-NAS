package domain

import (
	"encoding/binary"
	"errors"
)

// Rates the analysis reads the song at (ffmpegworker.AudioRequest.PCM).
const (
	StereoRate = 44100
	MonoRate   = 22050
)

// EncodeLoudness is the curve as little-endian float32, as the Workers pass
// it and the songs table keeps it.
func EncodeLoudness(values []float32) []byte {
	if values == nil {
		return nil
	}
	// Append fails only for types without a fixed size.
	out, _ := binary.Append(make([]byte, 0, 4*len(values)), binary.LittleEndian, values)
	return out
}

// DecodeLoudness reads a curve EncodeLoudness wrote.
func DecodeLoudness(data []byte) ([]float32, error) {
	if len(data)%4 != 0 {
		return nil, errors.New("a song's loudness is not whole float32 values")
	}
	out := make([]float32, len(data)/4)
	if _, err := binary.Decode(data, binary.LittleEndian, out); err != nil {
		return nil, err
	}
	return out, nil
}
