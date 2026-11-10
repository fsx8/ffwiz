package app

import (
	"testing"

	ffx "github.com/fsx8/ffwiz/internal/ffmpeg"
	"github.com/fsx8/ffwiz/internal/ffprobe"
)

func TestHDRNoteFor(t *testing.T) {
	cases := []struct {
		name    string
		stream  ffprobe.Stream
		wantSub string // empty means no note expected
	}{
		{"SDR 8-bit", ffprobe.Stream{PixFmt: "yuv420p"}, ""},
		{"HDR10 10-bit", ffprobe.Stream{PixFmt: "yuv420p10le", ColorTransfer: "smpte2084"}, "HDR10"},
		{"HLG", ffprobe.Stream{PixFmt: "yuv420p10le", ColorTransfer: "arib-std-b67"}, "HLG"},
		{"10-bit SDR", ffprobe.Stream{PixFmt: "yuv420p10le"}, "10-bit"},
		{"12-bit", ffprobe.Stream{PixFmt: "yuv420p12be"}, "12-bit"},
	}
	for _, c := range cases {
		got := hdrNoteFor(c.stream)
		if c.wantSub == "" {
			if got != "" {
				t.Errorf("%s: expected no note, got %q", c.name, got)
			}
			continue
		}
		if got == "" || !contains(got, c.wantSub) {
			t.Errorf("%s: note %q should mention %q", c.name, got, c.wantSub)
		}
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// The preserve note must describe the new behavior (10-bit HEVC, HDR kept,
// no tone mapping) instead of the old crushing warning.
func TestPreserveNoteFor(t *testing.T) {
	cases := []struct {
		name     string
		transfer string
		pixFmt   string
		wantSub  []string
		wantNone string
	}{
		{"HDR10", "smpte2084", "yuv420p10le",
			[]string{"HDR10", "10-bit HEVC", "no tone mapping"}, "8-bit SDR"},
		{"HLG", "arib-std-b67", "yuv420p10le",
			[]string{"HLG", "10-bit HEVC"}, "8-bit SDR"},
		{"10-bit SDR keeps the depth", "", "yuv420p10le",
			[]string{"10-bit", "10-bit HEVC"}, "8-bit SDR"},
		{"SDR stays quiet", "", "yuv420p", nil, "Note:"},
	}
	for _, c := range cases {
		got := preserveNoteFor(c.transfer, c.pixFmt)
		if c.wantSub == nil {
			if got != "" {
				t.Errorf("%s: expected no note, got %q", c.name, got)
			}
			continue
		}
		for _, sub := range c.wantSub {
			if !contains(got, sub) {
				t.Errorf("%s: note %q should mention %q", c.name, got, sub)
			}
		}
		if contains(got, c.wantNone) {
			t.Errorf("%s: note %q must not mention %q", c.name, got, c.wantNone)
		}
	}
}

// The crushing warning (Modify Tracks convert to an 8-bit codec) keeps the
// washed-out wording without the stream-copy hint, which is meaningless
// inside Modify Tracks itself.
func TestCrushNoteFor(t *testing.T) {
	got := crushNoteFor("smpte2084", "yuv420p10le")
	for _, sub := range []string{"HDR10", "8-bit SDR", "washed out"} {
		if !contains(got, sub) {
			t.Errorf("crush note %q should mention %q", got, sub)
		}
	}
	if contains(got, "Use Modify Tracks") {
		t.Errorf("crush note must not send the user to Modify Tracks from within it: %q", got)
	}
	if got := crushNoteFor("", "yuv420p"); got != "" {
		t.Errorf("plain SDR must not warn, got %q", got)
	}
}

// The Modify Tracks note fires only while a conversion is armed on an
// HDR/10-bit video track: crushing codecs warn, libx265 informs.
func TestHDRConvertNote(t *testing.T) {
	hdrVideo := trackView{Track: ffx.Track{
		Index: 0, Type: ffx.TrackVideo, Codec: "hevc",
		Color: ffx.ColorProps{PixFmt: "yuv420p10le", Transfer: "smpte2084", Primaries: "bt2020"},
	}}
	sdrVideo := trackView{Track: ffx.Track{Index: 0, Type: ffx.TrackVideo, Codec: "h264"}}
	audio := trackView{Track: ffx.Track{Index: 1, Type: ffx.TrackAudio, Codec: "aac"}}

	cases := []struct {
		name    string
		tracks  []trackView
		actions map[int]ffx.TrackActionInfo
		wantSub string // empty means no note expected
	}{
		{"nothing armed", []trackView{hdrVideo, audio}, map[int]ffx.TrackActionInfo{}, ""},
		{"keep armed", []trackView{hdrVideo}, map[int]ffx.TrackActionInfo{
			0: {Action: ffx.ActionKeep},
		}, ""},
		{"SDR conversion stays quiet", []trackView{sdrVideo}, map[int]ffx.TrackActionInfo{
			0: {Action: ffx.ActionConvert, Codec: "libx264 (H.264)"},
		}, ""},
		{"audio conversions are ignored", []trackView{hdrVideo, audio}, map[int]ffx.TrackActionInfo{
			1: {Action: ffx.ActionConvert, Codec: "libmp3lame (MP3)"},
		}, ""},
		{"x264 crushes", []trackView{hdrVideo}, map[int]ffx.TrackActionInfo{
			0: {Action: ffx.ActionConvert, Codec: "libx264 (H.264)"},
		}, "8-bit SDR"},
		{"vp9 crushes", []trackView{hdrVideo}, map[int]ffx.TrackActionInfo{
			0: {Action: ffx.ActionConvert, Codec: "libvpx-vp9 (VP9)"},
		}, "8-bit SDR"},
		{"x265 preserves", []trackView{hdrVideo}, map[int]ffx.TrackActionInfo{
			0: {Action: ffx.ActionConvert, Codec: "libx265 (H.265/HEVC)"},
		}, "10-bit HEVC"},
	}
	for _, c := range cases {
		got := hdrConvertNote(c.tracks, c.actions)
		if c.wantSub == "" {
			if got != "" {
				t.Errorf("%s: expected no note, got %q", c.name, got)
			}
			continue
		}
		if !contains(got, c.wantSub) {
			t.Errorf("%s: note %q should mention %q", c.name, got, c.wantSub)
		}
	}
}
