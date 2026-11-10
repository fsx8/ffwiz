package ffmpeg

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// SpeedOutputName derives the default output name for a speed change:
// <base>_<factor>x<ext> with the minimal decimal form of the factor,
// e.g. clip.mp4 + 2 -> clip_2x.mp4, clip.mp4 + 1.25 -> clip_1.25x.mp4.
func SpeedOutputName(inputPath string, factor float64) string {
	return transformOutputName(inputPath, strconv.FormatFloat(factor, 'f', -1, 64)+"x")
}

// ReverseOutputName derives the default output name for a reversed copy:
// <base>_reversed<ext>, e.g. clip.mp4 -> clip_reversed.mp4.
func ReverseOutputName(inputPath string) string {
	return transformOutputName(inputPath, "reversed")
}

// MuteOutputName derives the default output name for a muted copy:
// <base>_muted<ext>, e.g. clip.mp4 -> clip_muted.mp4.
func MuteOutputName(inputPath string) string {
	return transformOutputName(inputPath, "muted")
}

// formatTempo renders an atempo value with at least one decimal, matching
// the canonical filter spelling (2 -> "2.0", 0.5 -> "0.5").
func formatTempo(v float64) string {
	s := strconv.FormatFloat(v, 'f', -1, 64)
	if !strings.Contains(s, ".") {
		s += ".0"
	}
	return s
}

// atempoChain splits an arbitrary speed factor into atempo elements that
// each stay inside the filter's accepted range of 0.5-2.0, e.g.
// 4 -> [2 2] and 0.25 -> [0.5 0.5]; the element product equals the factor.
func atempoChain(factor float64) []float64 {
	var n int
	switch {
	case factor > 2.0:
		n = int(math.Ceil(factor / 2.0))
	case factor < 0.5:
		n = int(math.Ceil(0.5 / factor))
	default:
		return []float64{factor}
	}
	var e float64
	if n == 2 {
		e = math.Sqrt(factor)
	} else {
		e = math.Pow(factor, 1/float64(n))
	}
	chain := make([]float64, n)
	for i := range chain {
		chain[i] = e
	}
	return chain
}

func atempoFilter(factor float64) string {
	chain := atempoChain(factor)
	parts := make([]string, len(chain))
	for i, v := range chain {
		parts[i] = "atempo=" + formatTempo(v)
	}
	return strings.Join(parts, ",")
}

// audioFilterChains renders one filter chain per audio stream, labelling
// the results a0..aN-1. The per-stream sources bind 0:a:N because plain
// [0:a] resolves to only the FIRST audio stream — the other tracks would
// silently vanish from the output.
func audioFilterChains(filter string, n int) string {
	parts := make([]string, n)
	for i := range parts {
		parts[i] = fmt.Sprintf("[0:a:%d]%s[a%d]", i, filter, i)
	}
	return strings.Join(parts, ";")
}

// BuildSpeedCmd changes playback speed by the given factor via setpts for
// video and a chained atempo filter per audio stream. Factors outside
// 0.25-4.0 yield a zero Cmd. atempo requires re-encoding, so audio is
// encoded to AAC at 192k; when hasAudio is false the audio branch is
// omitted entirely. audioStreams is the probed audio track count and every
// track is filtered and mapped so multi-track files keep all layers; a
// count < 1 (probe failed) falls back to the legacy single-track behavior.
// An optional trailing ColorProps (the probed color format) switches the
// video branch to 10-bit HEVC with the source's HDR signaling laid in via
// setparams; omitting it keeps the historical 8-bit x264 command.
func BuildSpeedCmd(inputPath string, factor float64, hasAudio bool, audioStreams int, outputPath string, color ...ColorProps) Cmd {
	if factor < 0.25 || factor > 4.0 {
		return Cmd{}
	}
	n := audioStreamCount(hasAudio, audioStreams)
	factorStr := strconv.FormatFloat(factor, 'f', -1, 64)
	if c := hdrProps(color); c.HDR() {
		graph := fmt.Sprintf("[0:v]setpts=PTS/%s,%s[v]", factorStr, c.FilterSuffix())
		if hasAudio {
			graph += ";" + audioFilterChains(atempoFilter(factor), n)
		}
		args := []string{"-i", inputPath, "-filter_complex", graph, "-map", "[v]"}
		if hasAudio {
			args = appendAudioMaps(args, n)
		}
		args = append(args, x265HDRArgs(c, "22", "medium")...)
		if hasAudio {
			args = append(args, "-c:a", "aac", "-b:a", "192k")
		}
		return Cmd{Args: append(args, "-y", outputPath)}
	}
	graph := fmt.Sprintf("[0:v]setpts=PTS/%s[v]", factorStr)
	if hasAudio {
		graph += ";" + audioFilterChains(atempoFilter(factor), n)
	}
	args := []string{"-i", inputPath, "-filter_complex", graph, "-map", "[v]"}
	if hasAudio {
		args = appendAudioMaps(args, n)
	}
	args = append(args,
		"-c:v", "libx264",
		"-crf", "23",
		"-preset", "medium",
		"-pix_fmt", "yuv420p",
	)
	if hasAudio {
		args = append(args, "-c:a", "aac", "-b:a", "192k")
	}
	return Cmd{Args: append(args, "-y", outputPath)}
}

// BuildReverseCmd plays the video backwards, together with its audio when
// one exists. Both filters buffer the whole stream, so a full re-encode is
// required; audio is encoded to AAC at 192k when present. Every probed
// audio track (audioStreams) is reversed and mapped; a count < 1 falls
// back to the legacy single-track behavior. An optional trailing
// ColorProps (the probed color format) switches the video branch to
// 10-bit HEVC with the source's HDR signaling laid in via setparams;
// omitting it keeps the historical 8-bit x264 command.
func BuildReverseCmd(inputPath string, hasAudio bool, audioStreams int, outputPath string, color ...ColorProps) Cmd {
	c := hdrProps(color)
	vf := "reverse"
	if c.HDR() {
		vf += "," + c.FilterSuffix()
	}
	if !hasAudio {
		if c.HDR() {
			return Cmd{
				Args: appendHDRReencodeArgs([]string{"-i", inputPath, "-vf", vf}, c, outputPath),
			}
		}
		return Cmd{
			Args: appendReencodeArgs([]string{"-i", inputPath, "-vf", vf}, outputPath),
		}
	}
	n := audioStreamCount(hasAudio, audioStreams)
	graph := "[0:v]reverse[v];"
	if c.HDR() {
		graph = "[0:v]reverse," + c.FilterSuffix() + "[v];"
	}
	graph += audioFilterChains("areverse", n)
	args := append([]string{
		"-i", inputPath,
		"-filter_complex", graph,
		"-map", "[v]",
	}, appendAudioMaps(nil, n)...)
	if c.HDR() {
		args = append(args, x265HDRArgs(c, "22", "medium")...)
	} else {
		args = append(args,
			"-c:v", "libx264",
			"-crf", "23",
			"-preset", "medium",
			"-pix_fmt", "yuv420p",
		)
	}
	args = append(args,
		"-c:a", "aac",
		"-b:a", "192k",
	)
	return Cmd{Args: append(args, "-y", outputPath)}
}

// audioStreamCount resolves the number of audio tracks to process: a
// missing or failed probe (count < 1) degrades to a single track instead
// of producing a broken command.
func audioStreamCount(hasAudio bool, audioStreams int) int {
	if !hasAudio || audioStreams < 1 {
		return 1
	}
	return audioStreams
}

// appendAudioMaps adds one labelled map per audio filter output.
func appendAudioMaps(args []string, n int) []string {
	for i := 0; i < n; i++ {
		args = append(args, "-map", fmt.Sprintf("[a%d]", i))
	}
	return args
}

// BuildMuteCmd removes every audio track while stream-copying everything
// else, so no re-encode happens at all. The explicit maps keep every video
// (0:V excludes attached cover art) and every subtitle stream: with plain
// -an ffmpeg's automatic selection would retain only the "best" subtitle
// track and silently drop the rest.
func BuildMuteCmd(inputPath, outputPath string) Cmd {
	return Cmd{
		Args: []string{
			"-i", inputPath,
			"-map", "0:V?",
			"-map", "0:s?",
			"-an",
			"-c:v", "copy",
			"-c:s", "copy",
			"-y", outputPath,
		},
	}
}
