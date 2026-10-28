package mediaplay_test

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/mediaplay"
	"github.com/zhongwater123/A-NAS/internal/mediaworker"
)

func TestCapsAreReadFromThePlayersList(t *testing.T) {
	got := mediaplay.ParseCaps("hevc, av1,vp9,webm,mkv,ac3,eac3,flac,")
	want := mediaplay.Caps{HEVC: true, AV1: true, VP9: true, WebM: true, Matroska: true, AC3: true, EAC3: true}
	if got != want {
		t.Fatalf("ParseCaps() = %+v, want %+v", got, want)
	}
	if got := mediaplay.ParseCaps(""); got != (mediaplay.Caps{}) {
		t.Fatalf("ParseCaps(\"\") = %+v", got)
	}
}

// Videos from both the photo library (phones, cameras) and the media center
// (films, episodes) play the cheapest way the browser can.
func TestBrowsersPlayVideosTheCheapestWayTheyCan(t *testing.T) {
	const mp4, mkv = "mov,mp4,m4a,3gp,3g2,mj2", "matroska,webm"
	aac := []mediaplay.Audio{{Codec: "aac", Default: true}}
	ac3First := []mediaplay.Audio{{Codec: "ac3", Default: true}, {Codec: "aac"}}
	aacSecond := []mediaplay.Audio{{Codec: "aac"}, {Codec: "aac", Default: true}}
	second := 1
	h264 := &mediaplay.Video{Codec: "h264", PixelFormat: "yuv420p", Height: 1080}
	iphone := mediaplay.Source{Container: mp4, Video: &mediaplay.Video{Codec: "hevc", PixelFormat: "yuv420p10le", Height: 2160}, Audio: aac}
	android := mediaplay.Source{Container: mp4, Video: h264, Audio: aac}
	camcorder := mediaplay.Source{Container: "mpegts", Video: h264, Audio: []mediaplay.Audio{{Codec: "ac3"}}}
	oldCamera := mediaplay.Source{Container: "avi", Video: &mediaplay.Video{Codec: "mjpeg", PixelFormat: "yuvj422p"}, Audio: []mediaplay.Audio{{Codec: "pcm_s16le"}}}
	webm := mediaplay.Source{Container: mkv, Video: &mediaplay.Video{Codec: "vp9"}, Audio: []mediaplay.Audio{{Codec: "opus"}}}
	film := mediaplay.Source{Container: mkv, Video: h264, Audio: aac}
	sevenTwenty, _ := mediaplay.QualityNamed("720p")
	tenEighty, _ := mediaplay.QualityNamed("1080p")
	for _, test := range []struct {
		name   string
		source mediaplay.Source
		caps   mediaplay.Caps
		choice mediaplay.Choice
		mode   string
		why    mediaplay.Why
		mime   string
		check  func(mediaworker.StreamRequest) bool
	}{
		{name: "HEVC where the browser decodes it", source: iphone, caps: mediaplay.Caps{HEVC: true}, mode: mediaplay.Direct},
		{name: "HEVC where it does not", source: iphone, choice: mediaplay.Choice{Ceiling: tenEighty}, mode: mediaplay.Transcode, why: mediaplay.Picture,
			mime: `video/mp4; codecs="avc1.640033,mp4a.40.2"`,
			check: func(r mediaworker.StreamRequest) bool {
				return !r.CopyVideo && r.Height == 1080 && r.MaxBitrate == 8000 && r.CopyAudio
			}},
		{name: "H.264 in MP4", source: android, mode: mediaplay.Direct},
		{name: "a camcorder's transport stream", source: camcorder, mode: mediaplay.Remux, why: mediaplay.Sound, mime: `video/mp4; codecs="avc1.640033,mp4a.40.2"`,
			check: func(r mediaworker.StreamRequest) bool { return r.CopyVideo && !r.CopyAudio && r.Height == 0 }},
		{name: "a camcorder's stream where AC-3 plays", source: camcorder, caps: mediaplay.Caps{AC3: true}, mode: mediaplay.Remux, why: mediaplay.Container},
		{name: "an old camera's AVI", source: oldCamera, caps: mediaplay.Caps{HEVC: true}, mode: mediaplay.Transcode, why: mediaplay.Picture,
			mime: `video/mp4; codecs="avc1.640033,mp4a.40.2"`},
		{name: "WebM", source: webm, caps: mediaplay.Caps{VP9: true, WebM: true}, mode: mediaplay.Direct},
		{name: "WebM where only Matroska is reported", source: webm, caps: mediaplay.Caps{VP9: true, Matroska: true}, mode: mediaplay.Direct},
		{name: "Matroska in Chromium", source: film, caps: mediaplay.Caps{Matroska: true}, mode: mediaplay.Direct},
		{name: "Matroska elsewhere", source: film, mode: mediaplay.Remux, why: mediaplay.Container,
			check: func(r mediaworker.StreamRequest) bool { return r.CopyVideo && r.CopyAudio }},
		{name: "silent HEVC copied", source: mediaplay.Source{Container: "mpegts", Video: &mediaplay.Video{Codec: "hevc"}}, caps: mediaplay.Caps{HEVC: true},
			mode: mediaplay.Remux, why: mediaplay.Container, mime: `video/mp4; codecs="hvc1.1.6.L153.B0"`},
		{name: "a default AC-3 track", source: mediaplay.Source{Container: mkv, Video: h264, Audio: ac3First}, caps: mediaplay.Caps{Matroska: true},
			mode: mediaplay.Remux, why: mediaplay.Sound, check: func(r mediaworker.StreamRequest) bool { return r.AudioTrack == 0 && !r.CopyAudio }},
		{name: "the second audio track", source: mediaplay.Source{Container: mp4, Video: h264, Audio: ac3First}, caps: mediaplay.Caps{AC3: true},
			choice: mediaplay.Choice{AudioTrack: &second}, mode: mediaplay.Remux, why: mediaplay.OtherTrack,
			check: func(r mediaworker.StreamRequest) bool { return r.AudioTrack == 1 && r.CopyAudio }},
		{name: "a default second audio track", source: mediaplay.Source{Container: mp4, Video: h264, Audio: aacSecond}, mode: mediaplay.Direct,
			check: func(r mediaworker.StreamRequest) bool { return r.AudioTrack == 1 }},
		{name: "an audio stream FFmpeg could not name", source: mediaplay.Source{Container: mkv, Video: h264, Audio: []mediaplay.Audio{{Default: true}}},
			caps: mediaplay.Caps{Matroska: true}, mode: mediaplay.Remux, why: mediaplay.Sound,
			check: func(r mediaworker.StreamRequest) bool { return r.AudioTrack == 0 && !r.CopyAudio }},
		{name: "a lower quality", source: android, choice: mediaplay.Choice{Lower: sevenTwenty}, mode: mediaplay.Transcode,
			why: mediaplay.Lowered, check: func(r mediaworker.StreamRequest) bool { return r.Height == 720 && r.MaxBitrate == 4000 }},
		{name: "a quality the source does not exceed", source: android, choice: mediaplay.Choice{Lower: tenEighty},
			mode: mediaplay.Direct},
		{name: "10-bit H.264", source: mediaplay.Source{Container: mp4, Video: &mediaplay.Video{Codec: "h264", PixelFormat: "yuv420p10le"}, Audio: aac},
			mode: mediaplay.Transcode, why: mediaplay.Picture},
		{name: "3GPP with H.264", source: mediaplay.Source{Container: mp4, ThreeGPP: true, Video: h264, Audio: aac}, mode: mediaplay.Direct},
		{name: "3GPP with HEVC", source: mediaplay.Source{Container: mp4, ThreeGPP: true, Video: &mediaplay.Video{Codec: "hevc"}, Audio: aac},
			caps: mediaplay.Caps{HEVC: true}, mode: mediaplay.Remux, why: mediaplay.Container},
		{name: "a failed original", source: film, caps: mediaplay.Caps{Matroska: true},
			choice: mediaplay.Choice{Ceiling: tenEighty, Transcode: true}, mode: mediaplay.Transcode, why: mediaplay.Failed,
			check: func(r mediaworker.StreamRequest) bool { return !r.CopyVideo && r.CopyAudio && r.Height == 1080 }},
		{name: "a conversion without a ceiling", source: iphone, mode: mediaplay.Transcode,
			why: mediaplay.Picture, check: func(r mediaworker.StreamRequest) bool { return r.Height == 0 && r.MaxBitrate == 0 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			decision := mediaplay.Decide(test.source, test.caps, test.choice)
			if decision.Mode != test.mode || decision.Why != test.why {
				t.Fatalf("Decide() = %s for %d, want %s for %d", decision.Mode, decision.Why, test.mode, test.why)
			}
			if test.mime != "" {
				if mime := mediaplay.StreamMime(test.source, decision.Stream); mime != test.mime {
					t.Fatalf("mime = %s, want %s", mime, test.mime)
				}
			}
			if test.check != nil && !test.check(decision.Stream) {
				t.Fatalf("stream = %+v", decision.Stream)
			}
		})
	}
}

func TestAStreamOfAPlayableVideoIsAFullConversion(t *testing.T) {
	source := mediaplay.Source{Container: "mov,mp4", Video: &mediaplay.Video{Codec: "h264"}, Audio: []mediaplay.Audio{{Codec: "aac"}}}
	decision := mediaplay.Converting(source, mediaplay.Caps{}, mediaplay.Choice{Ceiling: mediaplay.Qualities()[0]})
	if decision.Mode != mediaplay.Transcode || decision.Why != mediaplay.Failed || decision.Stream.CopyVideo || decision.Stream.Height != 1080 {
		t.Fatalf("Converting() = %+v", decision)
	}
	remux := mediaplay.Source{Container: "avi", Video: &mediaplay.Video{Codec: "h264"}}
	if decision := mediaplay.Converting(remux, mediaplay.Caps{}, mediaplay.Choice{}); decision.Mode != mediaplay.Remux {
		t.Fatalf("Converting() of an AVI = %+v, want a remux", decision)
	}
}

func TestConvertedStreamsNameTheirCodecs(t *testing.T) {
	h264 := &mediaplay.Video{Codec: "h264"}
	hevc := &mediaplay.Video{Codec: "hevc"}
	for _, test := range []struct {
		source  mediaplay.Source
		request mediaworker.StreamRequest
		want    string
	}{
		{mediaplay.Source{Video: h264, Audio: []mediaplay.Audio{{Codec: "ac3"}}}, mediaworker.StreamRequest{CopyVideo: true}, `video/mp4; codecs="avc1.640033,mp4a.40.2"`},
		{mediaplay.Source{Video: hevc, Audio: []mediaplay.Audio{{Codec: "mp3"}}}, mediaworker.StreamRequest{CopyVideo: true, HEVC: true, CopyAudio: true},
			`video/mp4; codecs="hvc1.1.6.L153.B0,mp4a.69"`},
		{mediaplay.Source{Video: hevc}, mediaworker.StreamRequest{AudioTrack: -1}, `video/mp4; codecs="avc1.640033"`},
		{mediaplay.Source{Audio: []mediaplay.Audio{{Codec: "flac"}}}, mediaworker.StreamRequest{}, `video/mp4; codecs="mp4a.40.2"`},
	} {
		if got := mediaplay.StreamMime(test.source, test.request); got != test.want {
			t.Errorf("StreamMime(%+v) = %s, want %s", test.request, got, test.want)
		}
	}
}

func TestStreamsStartWithinTheVideo(t *testing.T) {
	for _, test := range []struct {
		start, duration float64
		ok              bool
	}{
		{0, 10, true}, {9.9, 10, true}, {10, 10, false}, {-1, 10, false},
		{math.NaN(), 10, false}, {math.Inf(1), 0, false}, {600, 0, true},
	} {
		if got := mediaplay.StartWithin(test.start, test.duration); got != test.ok {
			t.Errorf("StartWithin(%v, %v) = %v", test.start, test.duration, got)
		}
	}
}

func TestConversionsWaitBrieflyForAFreeSlot(t *testing.T) {
	ctx := context.Background()
	full := func(wait time.Duration) (*mediaplay.Slots, func()) {
		t.Helper()
		slots := mediaplay.NewSlots(2, wait)
		first, err := slots.Take(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := slots.Take(ctx); err != nil {
			t.Fatal(err)
		}
		return slots, first
	}

	busy, _ := full(50 * time.Millisecond)
	began := time.Now()
	if _, err := busy.Take(ctx); !errors.Is(err, mediaplay.ErrBusy) {
		t.Fatalf("a third conversion: %v, want ErrBusy", err)
	}
	if waited := time.Since(began); waited < 40*time.Millisecond {
		t.Fatalf("a full device answered busy after %v, without waiting", waited)
	}

	// A seek's stream gets the slot of the stream it replaces as it closes,
	// however late the closing stream is scheduled within the wait.
	seeking, first := full(time.Minute)
	go func() { time.Sleep(5 * time.Millisecond); first(); first() }()
	if _, err := seeking.Take(ctx); err != nil {
		t.Fatalf("a seek replacing a stream did not get its slot: %v", err)
	}
	waiting, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := seeking.Take(waiting); !errors.Is(err, context.Canceled) {
		t.Fatalf("releasing twice freed two slots: %v", err)
	}
}

func TestTheQualityListIsNotTheCallersToChange(t *testing.T) {
	list := mediaplay.Qualities()
	list[0].Height = 1
	if best := mediaplay.Qualities()[0]; best.Height != 1080 {
		t.Fatalf("the best quality became %+v after a caller changed its copy", best)
	}
}
