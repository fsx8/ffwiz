package ffmpeg

import (
	"strconv"
	"strings"
)

// This file holds the HDR/10-bit preserving re-encode branch shared by the
// video generators. The mechanism is deliberately filter-based: the
// setparams filter lays the source's color signaling into the frames as
// side data, which every ffmpeg version muxes — the -color_* output flags
// do NOT reach the muxer on ffmpeg 7.1+/8.x anymore, so they must not be
// used here.

// ColorProps describes the source video's color characteristics as probed
// by ffprobe; generators take it in instead of probing (they stay pure).
// The zero value is an ordinary 8-bit SDR stream and keeps the historical
// 8-bit x264 commands.
type ColorProps struct {
	Primaries string // color_primaries, forwarded verbatim (bt2020, bt709, …)
	Transfer  string // color_transfer (smpte2084, arib-std-b67, …)
	Space     string // color_space (bt2020nc, …)
	PixFmt    string // pix_fmt, only the bit depth is interpreted

	// Master and Light carry probed HDR10 metadata; nil when the probe
	// found none (older ffprobe versions do not surface side data at all,
	// which simply omits the x265 metadata parameters).
	Master *MasterDisplayMeta
	Light  *ContentLightMeta
}

// MasterDisplayMeta mirrors mastering-display side data in the scaled
// integer spelling x265 expects: chromaticity coordinates in 0.00002
// increments, luminance in 0.0001 cd/m².
type MasterDisplayMeta struct {
	RedX, RedY     int64
	GreenX, GreenY int64
	BlueX, BlueY   int64
	WhiteX, WhiteY int64
	MinLum, MaxLum int64
}

// ContentLightMeta is the content light level side data (cd/m² integers).
type ContentLightMeta struct {
	MaxCLL  int64
	MaxFALL int64
}

// HDR reports whether the source needs the 10-bit preserving branch: a
// bit depth beyond 8, or an HDR transfer function regardless of depth.
func (c ColorProps) HDR() bool {
	return IsHDRVideo(c.PixFmt, c.Transfer)
}

// IsHDRVideo decides HDR/10-bit handling from the probed pix_fmt and
// color_transfer alone, so generators and the UI agree on one definition.
func IsHDRVideo(pixFmt, transfer string) bool {
	return PixDepth(pixFmt) > 8 || transfer == "smpte2084" || transfer == "arib-std-b67"
}

// FilterSuffix renders the filter segment that re-asserts the source's
// color signaling and forces a 10-bit output format, e.g.
// "setparams=color_primaries=bt2020:color_trc=smpte2084:colorspace=bt2020nc,format=yuv420p10le".
// The probed values are forwarded verbatim (they are valid filter values);
// when none were probed only the format conversion remains.
func (c ColorProps) FilterSuffix() string {
	var params []string
	if c.Primaries != "" {
		params = append(params, "color_primaries="+c.Primaries)
	}
	if c.Transfer != "" {
		params = append(params, "color_trc="+c.Transfer)
	}
	if c.Space != "" {
		params = append(params, "colorspace="+c.Space)
	}
	if len(params) == 0 {
		return "format=yuv420p10le"
	}
	return "setparams=" + strings.Join(params, ":") + ",format=yuv420p10le"
}

// X265Params renders the -x265-params value that makes x265 write the
// HDR10 SEI (master-display and max-cll), or "" when the probe found no
// metadata to forward. Coordinates are 0.00002-scaled integers, luminance
// in 0.0001 cd/m².
func (c ColorProps) X265Params() string {
	if c.Master == nil {
		return ""
	}
	md := "master-display=" + formatMasterDisplay(*c.Master)
	if c.Light != nil {
		md += ":max-cll=" + strconv.FormatInt(c.Light.MaxCLL, 10) + "," + strconv.FormatInt(c.Light.MaxFALL, 10)
	}
	return "hdr10=1:" + md
}

func formatMasterDisplay(m MasterDisplayMeta) string {
	parts := []string{
		"G(" + strconv.FormatInt(m.GreenX, 10) + "," + strconv.FormatInt(m.GreenY, 10) + ")",
		"B(" + strconv.FormatInt(m.BlueX, 10) + "," + strconv.FormatInt(m.BlueY, 10) + ")",
		"R(" + strconv.FormatInt(m.RedX, 10) + "," + strconv.FormatInt(m.RedY, 10) + ")",
		"WP(" + strconv.FormatInt(m.WhiteX, 10) + "," + strconv.FormatInt(m.WhiteY, 10) + ")",
		"L(" + strconv.FormatInt(m.MaxLum, 10) + "," + strconv.FormatInt(m.MinLum, 10) + ")",
	}
	return strings.Join(parts, "")
}

// x265HDRArgs is the video encoding tail of the HDR branch: 10-bit HEVC
// via the format filter (no -pix_fmt), with the HDR10 metadata parameters
// when the probe surfaced any.
func x265HDRArgs(c ColorProps, crf, preset string) []string {
	args := []string{"-c:v", "libx265", "-crf", crf, "-preset", preset}
	if p := c.X265Params(); p != "" {
		args = append(args, "-x265-params", p)
	}
	return args
}

// appendHDRReencodeArgs appends the tail used by the simple -vf generators
// (resize/transform/reverse-without-audio): 10-bit HEVC at the standard
// quality, audio stream-copied.
func appendHDRReencodeArgs(args []string, c ColorProps, outputPath string) []string {
	args = append(args, x265HDRArgs(c, "22", "medium")...)
	return append(args, "-c:a", "copy", "-y", outputPath)
}

// hdrProps extracts the optional trailing ColorProps from a generator's
// variadic slot; absent means "no probe data" and keeps the SDR command.
func hdrProps(color []ColorProps) ColorProps {
	if len(color) > 0 {
		return color[0]
	}
	return ColorProps{}
}

// knownPixDepths pins formats whose names a digit heuristic would misread:
// y210, x2rgb10, and xv30 are 10-bit despite their trailing digits, and
// rgb48/bgr48 use bits-per-pixel naming (16 bits per channel).
var knownPixDepths = map[string]int{
	"y210":    10,
	"x2rgb10": 10,
	"xv30":    10,
	"rgb48":   48,
	"bgr48":   48,
}

// PixDepth extracts the bit depth from a pix_fmt name (yuv420p10le -> 10);
// unrecognized names report 8. Shared by the generators and the UI so an
// unrecognized format can never be classified differently in each.
func PixDepth(pixFmt string) int {
	for _, suffix := range []string{"", "le", "be"} {
		p := strings.TrimSuffix(pixFmt, suffix)
		if d, ok := knownPixDepths[p]; ok {
			return d
		}
		// Planar formats spell the depth after the final "p"
		// (yuv420p10le -> yuv420p10 -> 10).
		if i := strings.LastIndex(p, "p"); i >= 0 {
			if n, err := strconv.Atoi(p[i+1:]); err == nil && n > 0 {
				return n
			}
			continue
		}
		// Packed gray/alpha formats carry the depth as trailing digits
		// (gray10le -> gray10 -> 10); other packed families (nv12,
		// rgb565le) encode layout, not depth, and stay unrecognized.
		if !strings.HasPrefix(p, "gray") && !strings.HasPrefix(p, "ya") {
			continue
		}
		digits := p
		for len(digits) > 0 && digits[len(digits)-1] >= '0' && digits[len(digits)-1] <= '9' {
			digits = digits[:len(digits)-1]
		}
		if len(digits) < len(p) {
			if n, err := strconv.Atoi(p[len(digits):]); err == nil && n > 0 {
				return n
			}
		}
	}
	return 8
}
