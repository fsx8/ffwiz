package app

import (
	"context"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	ffx "github.com/fsx8/ffwiz/internal/ffmpeg"
	ffprobe "github.com/fsx8/ffwiz/internal/ffprobe"
)

type hdrProbeMsg struct {
	note  string
	props ffx.ColorProps // generator input for the HDR-preserving branch
}

// hdrWarner carries the async color-format probe shared by the wizards
// that re-encode video (compress, resize, transform). The probe both
// informs the user (a note describing what happens to HDR) and feeds the
// generators' HDR-preserving 10-bit branch; an unreadable file simply
// produces neither.
type hdrWarner struct {
	note   string
	props  ffx.ColorProps
	cancel context.CancelFunc // aborts the in-flight probe (set by begin)
}

// begin starts the off-UI-thread probe; it never fails the wizard — an
// unreadable file simply produces no note. noteFor decides which note the
// wizard shows (preserve vs crush, depending on what its command does to
// HDR). The context is created before the command is returned and kept in
// cancel, so quitting the wizard aborts the probe instead of letting it
// run to completion.
func (h *hdrWarner) begin(prober ffprobe.Prober, path string, noteFor func(ffprobe.Stream) string) tea.Cmd {
	h.note = ""
	h.props = ffx.ColorProps{}
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	h.cancel = cancel
	return func() tea.Msg {
		defer cancel()
		if prober == nil {
			return hdrProbeMsg{}
		}
		res, err := prober.Probe(ctx, path)
		if err != nil || res == nil {
			return hdrProbeMsg{}
		}
		if s := res.FirstVideo(); s != nil {
			return hdrProbeMsg{note: noteFor(*s), props: colorProps(*s)}
		}
		return hdrProbeMsg{}
	}
}

// cancelProbe aborts an in-flight probe, if one is running.
func (h *hdrWarner) cancelProbe() {
	if h.cancel != nil {
		h.cancel()
		h.cancel = nil
	}
}

// apply folds a probe result in and reports whether a note was found.
func (h *hdrWarner) apply(msg hdrProbeMsg) bool {
	h.note = msg.note
	h.props = msg.props
	return h.note != ""
}

// colorProps converts a probed video stream into the generator input for
// the HDR-preserving branch. Mastering-display values are forwarded in the
// scaled integer spelling x265 expects (coordinates 0.00002, luminance
// 0.0001 cd/m²); files without side data simply carry no metadata.
func colorProps(s ffprobe.Stream) ffx.ColorProps {
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

// describeColor renders the "HDR10 10-bit yuv420p10le" style description
// shared by the note variants; empty for plain 8-bit SDR input.
func describeColor(transfer, pixFmt string) string {
	hdr := ""
	switch transfer {
	case "smpte2084":
		hdr = "HDR10"
	case "arib-std-b67":
		hdr = "HLG"
	}
	depth := ffx.PixDepth(pixFmt)

	var what []string
	if hdr != "" {
		what = append(what, hdr)
	}
	if depth > 8 {
		what = append(what, strconv.Itoa(depth)+"-bit "+pixFmt)
	}
	if len(what) == 0 {
		return ""
	}
	return strings.Join(what, " ")
}

// crushNoteFor renders the core of the washed-out warning for HDR / >8-bit
// sources that still get crushed to 8-bit SDR; empty for plain 8-bit SDR.
func crushNoteFor(transfer, pixFmt string) string {
	what := describeColor(transfer, pixFmt)
	if what == "" {
		return ""
	}
	return "Note: source is " + what +
		". The output will be re-encoded to 8-bit SDR (yuv420p) without tone mapping and may look washed out."
}

// hdrNoteFor renders the user-facing crushing warning; empty for plain
// 8-bit SDR input. Used where the command really does crush (Modify
// Tracks conversions to an 8-bit codec).
func hdrNoteFor(s ffprobe.Stream) string {
	note := crushNoteFor(s.ColorTransfer, s.PixFmt)
	if note == "" {
		return ""
	}
	switch s.ColorTransfer {
	case "smpte2084", "arib-std-b67":
		note += " Use Modify Tracks with a stream copy to keep HDR intact."
	}
	return note
}

// preserveNoteFor renders the informational note for the features that
// preserve HDR (compress/resize/transform/speed/reverse, and a libx265
// conversion in Modify Tracks): they re-encode in 10-bit HEVC with the
// source's signaling kept, so the old crushing warning would describe the
// wrong behavior.
func preserveNoteFor(transfer, pixFmt string) string {
	what := describeColor(transfer, pixFmt)
	if what == "" {
		return ""
	}
	note := "Note: source is " + what
	switch transfer {
	case "smpte2084", "arib-std-b67":
		return note + ". It will be re-encoded in 10-bit HEVC with HDR metadata preserved (no tone mapping applied)."
	default:
		return note + ". It will be re-encoded in 10-bit HEVC, preserving the extra bit depth."
	}
}

// hdrPreserveNoteFor is preserveNoteFor in stream form; it doubles as the
// note builder the hdrWarner probes use.
func hdrPreserveNoteFor(s ffprobe.Stream) string {
	return preserveNoteFor(s.ColorTransfer, s.PixFmt)
}
