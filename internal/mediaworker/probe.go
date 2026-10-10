package mediaworker

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/zhongwater123/A-NAS/internal/media"
)

type ffprobeOutput struct {
	Streams []struct {
		Index         int    `json:"index"`
		CodecType     string `json:"codec_type"`
		CodecName     string `json:"codec_name"`
		Profile       string `json:"profile"`
		Width         int    `json:"width"`
		Height        int    `json:"height"`
		PixelFormat   string `json:"pix_fmt"`
		Channels      int    `json:"channels"`
		AverageRate   string `json:"avg_frame_rate"`
		BaseRate      string `json:"r_frame_rate"`
		ColorTransfer string `json:"color_transfer"`
		SideData      []struct {
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
		Name     string `json:"format_name"`
		Duration string `json:"duration"`
		BitRate  string `json:"bit_rate"`
	} `json:"format"`
}

// ParseProbe turns ffprobe's JSON into what the media center keeps.
func ParseProbe(data []byte) (media.MediaInfo, error) {
	var output ffprobeOutput
	if err := json.Unmarshal(data, &output); err != nil {
		return media.MediaInfo{}, err
	}
	if output.Format.Name == "" {
		return media.MediaInfo{}, errors.New("ffprobe found no container")
	}
	info := media.MediaInfo{Container: output.Format.Name}
	info.Duration, _ = strconv.ParseFloat(output.Format.Duration, 64)
	info.Bitrate, _ = strconv.ParseInt(output.Format.BitRate, 10, 64)
	for _, stream := range output.Streams {
		language := stream.Tags["language"]
		if language == "und" {
			language = ""
		}
		switch stream.CodecType {
		case "video":
			if info.Video != nil || stream.Disposition.AttachedPic == 1 || stream.CodecName == "mjpeg" || stream.CodecName == "png" {
				continue
			}
			video := &media.VideoStream{
				Index: stream.Index, Codec: stream.CodecName, Profile: stream.Profile, Width: stream.Width, Height: stream.Height,
				PixelFormat: stream.PixelFormat, FrameRate: frameRate(stream.AverageRate),
			}
			if video.FrameRate == 0 {
				video.FrameRate = frameRate(stream.BaseRate)
			}
			switch stream.ColorTransfer {
			case "smpte2084":
				video.HDR = "HDR10"
			case "arib-std-b67":
				video.HDR = "HLG"
			}
			for _, side := range stream.SideData {
				if strings.Contains(side.Type, "DOVI") || strings.Contains(side.Type, "Dolby Vision") {
					video.HDR = "Dolby Vision"
				}
			}
			info.Video = video
		case "audio":
			info.Audio = append(info.Audio, media.AudioStream{
				Index: stream.Index, Codec: stream.CodecName, Channels: stream.Channels, Language: language,
				Title: stream.Tags["title"], Default: stream.Disposition.Default == 1,
			})
		case "subtitle":
			info.Subtitles = append(info.Subtitles, media.SubtitleStream{
				Index: stream.Index, Codec: stream.CodecName, Language: language, Title: stream.Tags["title"],
				Default: stream.Disposition.Default == 1, Forced: stream.Disposition.Forced == 1,
			})
		}
	}
	return info, nil
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
