package mediaworker

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
)

// Probe is what ffprobe reports about a file. Apps decide what it means for
// them: the media center picks video, audio and subtitle streams, the music
// library reads tags and the audio format.
type Probe struct {
	Format  ProbeFormat
	Streams []ProbeStream
}

type ProbeFormat struct {
	Name     string
	Duration float64
	Bitrate  int64
	Tags     map[string]string
}

type ProbeStream struct {
	Index         int
	Type          string
	Codec         string
	Profile       string
	Width         int
	Height        int
	PixelFormat   string
	FrameRate     float64
	ColorTransfer string
	SideData      []string
	Channels      int
	SampleRate    int
	SampleFormat  string
	// Bits is the significant bits per sample, when the codec says.
	Bits        int
	Default     bool
	Forced      bool
	AttachedPic bool
	Tags        map[string]string
}

type ffprobeOutput struct {
	Streams []struct {
		Index            int    `json:"index"`
		CodecType        string `json:"codec_type"`
		CodecName        string `json:"codec_name"`
		Profile          string `json:"profile"`
		Width            int    `json:"width"`
		Height           int    `json:"height"`
		PixelFormat      string `json:"pix_fmt"`
		Channels         int    `json:"channels"`
		SampleRate       string `json:"sample_rate"`
		SampleFormat     string `json:"sample_fmt"`
		BitsPerRawSample string `json:"bits_per_raw_sample"`
		AverageRate      string `json:"avg_frame_rate"`
		BaseRate         string `json:"r_frame_rate"`
		ColorTransfer    string `json:"color_transfer"`
		SideData         []struct {
			Type string `json:"side_data_type"`
		} `json:"side_data_list"`
		Disposition struct {
			Default     int `json:"default"`
			Forced      int `json:"forced"`
			AttachedPic int `json:"attached_pic"`
		} `json:"disposition"`
		Tags map[string]string `json:"tags"`
	} `json:"streams"`
	Format struct {
		Name     string            `json:"format_name"`
		Duration string            `json:"duration"`
		BitRate  string            `json:"bit_rate"`
		Tags     map[string]string `json:"tags"`
	} `json:"format"`
}

// ParseProbe reads ffprobe's JSON.
func ParseProbe(data []byte) (Probe, error) {
	var output ffprobeOutput
	if err := json.Unmarshal(data, &output); err != nil {
		return Probe{}, err
	}
	if output.Format.Name == "" {
		return Probe{}, errors.New("ffprobe found no container")
	}
	probe := Probe{Format: ProbeFormat{Name: output.Format.Name, Tags: output.Format.Tags}}
	probe.Format.Duration, _ = strconv.ParseFloat(output.Format.Duration, 64)
	probe.Format.Bitrate, _ = strconv.ParseInt(output.Format.BitRate, 10, 64)
	for _, stream := range output.Streams {
		parsed := ProbeStream{
			Index: stream.Index, Type: stream.CodecType, Codec: stream.CodecName, Profile: stream.Profile, Width: stream.Width,
			Height: stream.Height, PixelFormat: stream.PixelFormat, FrameRate: frameRate(stream.AverageRate),
			ColorTransfer: stream.ColorTransfer, Channels: stream.Channels, SampleFormat: stream.SampleFormat,
			Default: stream.Disposition.Default == 1, Forced: stream.Disposition.Forced == 1,
			AttachedPic: stream.Disposition.AttachedPic == 1, Tags: stream.Tags,
		}
		if parsed.FrameRate == 0 {
			parsed.FrameRate = frameRate(stream.BaseRate)
		}
		parsed.SampleRate, _ = strconv.Atoi(stream.SampleRate)
		parsed.Bits, _ = strconv.Atoi(stream.BitsPerRawSample)
		for _, side := range stream.SideData {
			parsed.SideData = append(parsed.SideData, side.Type)
		}
		probe.Streams = append(probe.Streams, parsed)
	}
	return probe, nil
}

// frameRate reads ffprobe's "num/den" rates.
func frameRate(value string) float64 {
	numerator, denominator, found := strings.Cut(value, "/")
	top, err := strconv.ParseFloat(numerator, 64)
	if err != nil {
		return 0
	}
	if !found {
		return top
	}
	bottom, err := strconv.ParseFloat(denominator, 64)
	if err != nil || bottom == 0 {
		return 0
	}
	return top / bottom
}
