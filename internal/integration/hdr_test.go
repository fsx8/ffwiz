//go:build integration

package integration

import (
	"path/filepath"
	"testing"
	"time"

	ffx "github.com/fsx8/ffwiz/internal/ffmpeg"
	ffprobe "github.com/fsx8/ffwiz/internal/ffprobe"
)

// hdrPropsFromProbe mirrors the app's colorProps mapping so this tier
// feeds the generators exactly what the wizards would.
func hdrPropsFromProbe(s ffprobe.Stream) ffx.ColorProps {
	c := ffx.ColorProps{
		Primaries: s.ColorPrimaries,
		Transfer:  s.ColorTransfer,
		Space:     s.ColorSpace,
		PixFmt:    s.PixFmt,
	}
	if md, ok := s.MasteringDisplay(); ok {
		c.Master = &ffx.MasterDisplayMeta{
			RedX:   md.RedX.Scaled(50000),
			RedY:   md.RedY.Scaled(50000),
			GreenX: md.GreenX.Scaled(50000),
			GreenY: md.GreenY.Scaled(50000),
			BlueX:  md.BlueX.Scaled(50000),
			BlueY:  md.BlueY.Scaled(50000),
			WhiteX: md.WhiteX.Scaled(50000),
			WhiteY: md.WhiteY.Scaled(50000),
			MinLum: md.MinLuminance.Scaled(10000),
			MaxLum: md.MaxLuminance.Scaled(10000),
		}
	}
	if cl, ok := s.ContentLight(); ok {
		c.Light = &ffx.ContentLightMeta{
			MaxCLL:  cl.MaxContent.Scaled(1),
			MaxFALL: cl.MaxAverage.Scaled(1),
		}
	}
	return c
}

func TestProbeHDRSmallFixture(t *testing.T) {
	requireTools(t)
	res := probeFile(t, fx(t, "hdr_small.mkv"))

	v := firstStream(t, res, "video")
	if v.CodecName != "hevc" || v.Width != 480 || v.Height != 270 {
		t.Fatalf("video = %s %dx%d, want hevc 480x270", v.CodecName, v.Width, v.Height)
	}
	if v.PixFmt != "yuv420p10le" {
		t.Fatalf("pix_fmt = %s, want yuv420p10le (10-bit)", v.PixFmt)
	}
	if v.ColorTransfer != "smpte2084" || v.ColorPrimaries != "bt2020" || v.ColorSpace != "bt2020nc" {
		t.Fatalf("HDR10 signaling missing: trc=%s primaries=%s space=%s", v.ColorTransfer, v.ColorPrimaries, v.ColorSpace)
	}
	a := firstStream(t, res, "audio")
	if a.CodecName != "aac" || a.Channels != 2 {
		t.Fatalf("audio = %s %dch, want stereo aac", a.CodecName, a.Channels)
	}
	assertDuration(t, res, 10, 1)

	// Note: neither ffprobe 6.1.1 (CI) nor 8.x emit stream_side_data_list
	// for an ffmpeg-muxed mkv — the x265 mastering-display SEI lives in
	// the bitstream, which -show_streams does not surface. The tolerant
	// parse of the side-data JSON itself is pinned by the ffprobe unit
	// tier (TestStreamSideDataParse); the generators must omit the
	// metadata parameters gracefully when probing finds none, which the
	// runs below prove end to end.
	if !hdrPropsFromProbe(v).HDR() {
		t.Fatal("the fixture must be detected as HDR/10-bit")
	}
}

// Speed on an HDR/10-bit source must stay 10-bit with the HDR10 signaling
// intact (not crushed to 8-bit SDR), halve the duration and keep audio.
func TestHDRSpeed2xPreservesTenBit(t *testing.T) {
	requireTools(t)
	in := fx(t, "hdr_small.mkv")
	props := hdrPropsFromProbe(*probeFile(t, in).FirstVideo())

	out := filepath.Join(t.TempDir(), "hdr_small_2x.mkv")
	cmd := ffx.BuildSpeedCmd(in, 2, true, 1, out, props)
	runFFmpeg(t, 3*time.Minute, cmd.Args)

	res := probeFile(t, out)
	assertDuration(t, res, 5, 0.5)
	if got := len(streamsOfType(res, "audio")); got != 1 {
		t.Fatalf("audio must survive the speed change, got %d streams", got)
	}
	v := firstStream(t, res, "video")
	if v.CodecName != "hevc" || v.PixFmt != "yuv420p10le" {
		t.Fatalf("video = %s %s, want 10-bit hevc (HDR must not be crushed)", v.CodecName, v.PixFmt)
	}
	if v.ColorTransfer != "smpte2084" || v.ColorPrimaries != "bt2020" {
		t.Fatalf("HDR10 signaling lost: trc=%s primaries=%s", v.ColorTransfer, v.ColorPrimaries)
	}
}

// Compression of an HDR/10-bit source re-encodes in 10-bit HEVC with the
// HDR10 signaling kept and audio stream-copied.
func TestHDRCompressPreservesHDR10(t *testing.T) {
	requireTools(t)
	in := fx(t, "hdr_small.mkv")
	props := hdrPropsFromProbe(*probeFile(t, in).FirstVideo())

	out := filepath.Join(t.TempDir(), "hdr_small_crf28.mkv")
	cmd := ffx.BuildCompressCmd(in, 28, "veryfast", out, props)
	runFFmpeg(t, 3*time.Minute, cmd.Args)

	res := probeFile(t, out)
	v := firstStream(t, res, "video")
	if v.CodecName != "hevc" || v.PixFmt != "yuv420p10le" {
		t.Fatalf("video = %s %s, want 10-bit hevc", v.CodecName, v.PixFmt)
	}
	if v.ColorTransfer != "smpte2084" || v.ColorPrimaries != "bt2020" {
		t.Fatalf("HDR10 signaling lost: trc=%s primaries=%s", v.ColorTransfer, v.ColorPrimaries)
	}
	if a := firstStream(t, res, "audio"); a.CodecName != "aac" {
		t.Fatalf("audio codec = %s, want aac (stream copy)", a.CodecName)
	}
	assertDuration(t, res, 10, 1)
}

// The tracks wizard's libx265 conversion of an HDR/10-bit video track must
// produce a 10-bit output with the HDR10 signaling intact.
func TestHDRInteractiveConvertToX265(t *testing.T) {
	requireTools(t)
	in := fx(t, "hdr_small.mkv")
	res := probeFile(t, in)

	tracks := tracksFromProbe(res)
	for i := range tracks {
		if tracks[i].Type == ffx.TrackVideo {
			tracks[i].Color = hdrPropsFromProbe(*res.FirstVideo())
		}
	}
	out := filepath.Join(t.TempDir(), "hdr_small_x265.mkv")
	cmd := ffx.BuildInteractiveConvertCmd(in, out, tracks,
		map[int]ffx.TrackActionInfo{0: {Action: ffx.ActionConvert, Codec: "libx265 (H.265/HEVC)"}})
	if cmd == nil {
		t.Fatal("expected a command")
	}
	runFFmpeg(t, 3*time.Minute, cmd.Args)

	v := firstStream(t, probeFile(t, out), "video")
	if v.CodecName != "hevc" || v.PixFmt != "yuv420p10le" {
		t.Fatalf("video = %s %s, want 10-bit hevc", v.CodecName, v.PixFmt)
	}
	if v.ColorTransfer != "smpte2084" || v.ColorPrimaries != "bt2020" {
		t.Fatalf("HDR10 signaling lost: trc=%s primaries=%s", v.ColorTransfer, v.ColorPrimaries)
	}
}
