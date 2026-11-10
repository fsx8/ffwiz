package ffmpeg

import (
	"testing"
)

// Shared HDR fixture for the generator tests: a fully-probed HDR10 source.
var testHDRProps = ColorProps{
	Primaries: "bt2020",
	Transfer:  "smpte2084",
	Space:     "bt2020nc",
	PixFmt:    "yuv420p10le",
	Master: &MasterDisplayMeta{
		GreenX: 8500, GreenY: 39850,
		BlueX: 6550, BlueY: 2300,
		RedX: 35400, RedY: 14600,
		WhiteX: 15635, WhiteY: 16450,
		MaxLum: 10000000, MinLum: 1,
	},
	Light: &ContentLightMeta{MaxCLL: 1000, MaxFALL: 400},
}

const testHDRSuffix = "setparams=color_primaries=bt2020:color_trc=smpte2084:colorspace=bt2020nc,format=yuv420p10le"

const testHDRParams = "hdr10=1:master-display=G(8500,39850)B(6550,2300)R(35400,14600)WP(15635,16450)L(10000000,1):max-cll=1000,400"

func TestPixDepth(t *testing.T) {
	cases := map[string]int{
		"yuv420p":     8,
		"yuv420p10le": 10,
		"yuv420p10be": 10,
		"yuv420p12le": 12,
		"yuv444p16le": 16,
		"nv12":        8,
		"p010le":      10, // semi-planar 10-bit layout
		"yuv420p10":   10,
		"gray10le":    10, // non-planar: digits are the depth
		"gray16le":    16,
		"rgb48le":     48, // 16 bits/channel, 48 bits/pixel naming
		"bgr48be":     48,
		"y210le":      10, // 10-bit despite the trailing 210
		"x2rgb10le":   10, // 10-bit despite the trailing 2
		"rgb565le":    8,  // layout code, not depth
		"junk":        8,
	}
	for fmt, want := range cases {
		if got := PixDepth(fmt); got != want {
			t.Errorf("PixDepth(%q) = %d, want %d", fmt, got, want)
		}
	}
}

// The one HDR definition shared by generators and UI: more than 8 bits or
// an HDR transfer function counts, plain 8-bit SDR does not.
func TestIsHDRVideo(t *testing.T) {
	cases := []struct {
		pixFmt   string
		transfer string
		want     bool
	}{
		{"yuv420p", "", false},
		{"yuv420p", "bt709", false},
		{"yuv420p10le", "", true},          // depth alone counts
		{"yuv420p10le", "bt709", true},     // 10-bit SDR still preserves depth
		{"yuv420p", "smpte2084", true},     // HDR signaling alone counts
		{"yuv420p", "arib-std-b67", true},  // HLG likewise
		{"yuv420p10le", "smpte2084", true}, // the classic HDR10 case
		{"yuv420p12le", "smpte2084", true}, //
	}
	for _, c := range cases {
		if got := IsHDRVideo(c.pixFmt, c.transfer); got != c.want {
			t.Errorf("IsHDRVideo(%q, %q) = %v, want %v", c.pixFmt, c.transfer, got, c.want)
		}
	}
}

// The setparams suffix forwards the probed values verbatim (they are valid
// filter values) and falls back to the bare format conversion when the
// probe found nothing.
func TestColorPropsFilterSuffix(t *testing.T) {
	full := ColorProps{
		Primaries: "bt2020",
		Transfer:  "smpte2084",
		Space:     "bt2020nc",
		PixFmt:    "yuv420p10le",
	}
	want := "setparams=color_primaries=bt2020:color_trc=smpte2084:colorspace=bt2020nc,format=yuv420p10le"
	if got := full.FilterSuffix(); got != want {
		t.Fatalf("FilterSuffix = %q, want %q", got, want)
	}
	partial := ColorProps{Transfer: "smpte2084", PixFmt: "yuv420p10le"}
	if got, want := partial.FilterSuffix(), "setparams=color_trc=smpte2084,format=yuv420p10le"; got != want {
		t.Fatalf("partial FilterSuffix = %q, want %q", got, want)
	}
	if got, want := (ColorProps{PixFmt: "yuv420p10le"}).FilterSuffix(), "format=yuv420p10le"; got != want {
		t.Fatalf("props-less FilterSuffix = %q, want %q", got, want)
	}
}

// The x265 metadata parameters carry the mastering display in x265's
// spelling and are omitted entirely when the probe found nothing.
func TestColorPropsX265Params(t *testing.T) {
	full := ColorProps{
		Master: &MasterDisplayMeta{
			GreenX: 8500, GreenY: 39850,
			BlueX: 6550, BlueY: 2300,
			RedX: 35400, RedY: 14600,
			WhiteX: 15635, WhiteY: 16450,
			MaxLum: 10000000, MinLum: 1,
		},
		Light: &ContentLightMeta{MaxCLL: 1000, MaxFALL: 400},
	}
	want := "hdr10=1:master-display=G(8500,39850)B(6550,2300)R(35400,14600)WP(15635,16450)L(10000000,1):max-cll=1000,400"
	if got := full.X265Params(); got != want {
		t.Fatalf("X265Params = %q, want %q", got, want)
	}
	masterOnly := ColorProps{Master: full.Master}
	want = "hdr10=1:master-display=G(8500,39850)B(6550,2300)R(35400,14600)WP(15635,16450)L(10000000,1)"
	if got := masterOnly.X265Params(); got != want {
		t.Fatalf("master-only X265Params = %q, want %q", got, want)
	}
	if got := (ColorProps{Light: full.Light}).X265Params(); got != "" {
		t.Fatalf("content light alone must not emit params, got %q", got)
	}
	if got := (ColorProps{}).X265Params(); got != "" {
		t.Fatalf("no metadata must omit the parameter, got %q", got)
	}
}
