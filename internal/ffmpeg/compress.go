package ffmpeg

import (
	"path/filepath"
	"strconv"
	"strings"
)

// CompressOutputName derives the default output name for a compressed copy:
// <base>_crf<N><ext>, e.g. clip.mp4 + 28 -> clip_crf28.mp4.
func CompressOutputName(inputPath string, crf int) string {
	ext := filepath.Ext(inputPath)
	base := strings.TrimSuffix(filepath.Base(inputPath), ext)
	return base + "_crf" + strconv.Itoa(crf) + ext
}

// BuildCompressCmd re-encodes the video with libx264 at the given CRF and
// x264 speed preset while stream-copying audio. The caller validates the
// CRF range (0-51) and the preset name. An optional trailing ColorProps
// (the probed color format) switches to 10-bit HEVC with the source's HDR
// signaling laid in via setparams, reusing the chosen CRF and preset
// verbatim (x264 preset names and the 0-51 CRF scale are valid for x265);
// omitting it keeps the historical 8-bit x264 command.
func BuildCompressCmd(inputPath string, crf int, preset string, outputPath string, color ...ColorProps) Cmd {
	if c := hdrProps(color); c.HDR() {
		args := append([]string{
			"-i", inputPath,
			"-vf", c.FilterSuffix(),
		}, x265HDRArgs(c, strconv.Itoa(crf), preset)...)
		return Cmd{
			Args: append(args, "-c:a", "copy", "-y", outputPath),
		}
	}
	return Cmd{
		Args: []string{
			"-i", inputPath,
			"-c:v", "libx264",
			"-crf", strconv.Itoa(crf),
			"-preset", preset,
			"-pix_fmt", "yuv420p",
			"-c:a", "copy",
			"-y", outputPath,
		},
	}
}
