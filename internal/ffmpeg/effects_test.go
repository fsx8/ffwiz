package ffmpeg

import (
	"reflect"
	"strings"
	"testing"
)

func TestSpeedOutputName(t *testing.T) {
	cases := []struct {
		factor float64
		want   string
	}{
		{2, "clip_2x.mp4"},
		{0.5, "clip_0.5x.mp4"},
		{1.25, "clip_1.25x.mp4"},
	}
	for _, c := range cases {
		if got := SpeedOutputName("clip.mp4", c.factor); got != c.want {
			t.Fatalf("SpeedOutputName(clip.mp4, %v) = %q, want %q", c.factor, got, c.want)
		}
	}
}

func TestReverseOutputName(t *testing.T) {
	if got, want := ReverseOutputName("x/y/clip.mp4"), "clip_reversed.mp4"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestMuteOutputName(t *testing.T) {
	if got, want := MuteOutputName("clip.mkv"), "clip_muted.mkv"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestBuildSpeedCmd_TwoTimesWithAudio(t *testing.T) {
	want := []string{
		"-i", "in.mp4",
		"-filter_complex", "[0:v]setpts=PTS/2[v];[0:a:0]atempo=2.0[a0]",
		"-map", "[v]",
		"-map", "[a0]",
		"-c:v", "libx264",
		"-crf", "23",
		"-preset", "medium",
		"-pix_fmt", "yuv420p",
		"-c:a", "aac",
		"-b:a", "192k",
		"-y", "out.mp4",
	}
	cmd := BuildSpeedCmd("in.mp4", 2, true, 1, "out.mp4")
	if !reflect.DeepEqual(cmd.Args, want) {
		t.Fatalf("got  %#v\nwant %#v", cmd.Args, want)
	}
}

// A multi-track file must keep every audio layer: the filter binds each
// track separately (plain [0:a] is only the first) and maps each output,
// while one -c:a applies to all of them.
func TestBuildSpeedCmd_MultiAudioKeepsEveryTrack(t *testing.T) {
	cmd := BuildSpeedCmd("in.mkv", 2, true, 3, "out.mkv")
	wantGraph := "[0:v]setpts=PTS/2[v];" +
		"[0:a:0]atempo=2.0[a0];[0:a:1]atempo=2.0[a1];[0:a:2]atempo=2.0[a2]"
	if got := flagValue(cmd.Args, "-filter_complex"); got != wantGraph {
		t.Fatalf("filter_complex = %q, want %q", got, wantGraph)
	}
	var maps []string
	for i, a := range cmd.Args {
		if a == "-map" {
			maps = append(maps, cmd.Args[i+1])
		}
	}
	wantMaps := []string{"[v]", "[a0]", "[a1]", "[a2]"}
	if !reflect.DeepEqual(maps, wantMaps) {
		t.Fatalf("maps = %#v, want %#v", maps, wantMaps)
	}
	if got := flagValue(cmd.Args, "-c:a"); got != "aac" {
		t.Fatalf("-c:a must apply to every track, got %q", got)
	}
}

func TestBuildSpeedCmd_UnknownAudioCountFallsBackToSingleTrack(t *testing.T) {
	single := BuildSpeedCmd("in.mp4", 2, true, 1, "out.mp4")
	fallback := BuildSpeedCmd("in.mp4", 2, true, 0, "out.mp4")
	if !reflect.DeepEqual(single.Args, fallback.Args) {
		t.Fatalf("a failed probe must degrade to the single-track command:\ngot  %#v\nwant %#v",
			fallback.Args, single.Args)
	}
}

func TestBuildSpeedCmd_FourTimesSplitsAtempoChain(t *testing.T) {
	cmd := BuildSpeedCmd("in.mp4", 4, true, 1, "out.mp4")
	if got, want := flagValue(cmd.Args, "-filter_complex"), "[0:v]setpts=PTS/4[v];[0:a:0]atempo=2.0,atempo=2.0[a0]"; got != want {
		t.Fatalf("filter_complex = %q, want %q", got, want)
	}
}

func TestBuildSpeedCmd_QuarterTimesSplitsAtempoChain(t *testing.T) {
	cmd := BuildSpeedCmd("in.mp4", 0.25, true, 1, "out.mp4")
	if got, want := flagValue(cmd.Args, "-filter_complex"), "[0:v]setpts=PTS/0.25[v];[0:a:0]atempo=0.5,atempo=0.5[a0]"; got != want {
		t.Fatalf("filter_complex = %q, want %q", got, want)
	}
}

func TestBuildSpeedCmd_WithoutAudioSkipsAudioBranch(t *testing.T) {
	cmd := BuildSpeedCmd("in.mp4", 1.5, false, 0, "out.mp4")
	if got, want := flagValue(cmd.Args, "-filter_complex"), "[0:v]setpts=PTS/1.5[v]"; got != want {
		t.Fatalf("filter_complex = %q, want %q", got, want)
	}
	for _, a := range cmd.Args {
		if a == "[a]" || a == "aac" || a == "192k" || strings.Contains(a, "atempo") {
			t.Fatalf("audio branch must be absent: %#v", cmd.Args)
		}
	}
	if got := flagValue(cmd.Args, "-c:a"); got != "" {
		t.Fatalf("no audio encoder expected, got -c:a %q", got)
	}
	if got := flagValue(cmd.Args, "-map"); got != "[v]" {
		t.Fatalf("expected a single -map [v], got %q", got)
	}
}

func TestBuildSpeedCmd_InvalidFactorYieldsZeroCmd(t *testing.T) {
	for _, f := range []float64{0.2, 4.5, 0, -1} {
		if cmd := BuildSpeedCmd("in.mp4", f, true, 1, "out.mp4"); len(cmd.Args) != 0 {
			t.Fatalf("factor %v must yield a zero Cmd, got %#v", f, cmd.Args)
		}
	}
}

// An HDR/10-bit source re-encodes in 10-bit HEVC with the setparams
// signaling composed into the same filtergraph and the x265 metadata
// parameters appended; no -pix_fmt is set (the format filter decides).
func TestBuildSpeedCmd_HDRSourceKeepsTenBit(t *testing.T) {
	want := []string{
		"-i", "in.mkv",
		"-filter_complex", "[0:v]setpts=PTS/2," + testHDRSuffix + "[v];[0:a:0]atempo=2.0[a0]",
		"-map", "[v]",
		"-map", "[a0]",
		"-c:v", "libx265",
		"-crf", "22",
		"-preset", "medium",
		"-x265-params", testHDRParams,
		"-c:a", "aac",
		"-b:a", "192k",
		"-y", "out.mkv",
	}
	cmd := BuildSpeedCmd("in.mkv", 2, true, 1, "out.mkv", testHDRProps)
	if !reflect.DeepEqual(cmd.Args, want) {
		t.Fatalf("got  %#v\nwant %#v", cmd.Args, want)
	}
}

// Without probed metadata the x265 parameters are simply omitted; the
// 10-bit branch itself stays.
func TestBuildSpeedCmd_HDRSourceWithoutMetadataOmitsX265Params(t *testing.T) {
	cmd := BuildSpeedCmd("in.mkv", 2, true, 1, "out.mkv", ColorProps{PixFmt: "yuv420p10le"})
	for _, a := range cmd.Args {
		if a == "-x265-params" {
			t.Fatalf("no metadata probed; must not emit -x265-params: %#v", cmd.Args)
		}
		if a == "-pix_fmt" {
			t.Fatalf("the HDR branch must not force -pix_fmt: %#v", cmd.Args)
		}
	}
	if got, want := flagValue(cmd.Args, "-filter_complex"), "[0:v]setpts=PTS/2,format=yuv420p10le[v];[0:a:0]atempo=2.0[a0]"; got != want {
		t.Fatalf("filter_complex = %q, want %q", got, want)
	}
	if got := flagValue(cmd.Args, "-c:v"); got != "libx265" {
		t.Fatalf("-c:v = %q, want libx265", got)
	}
}

func TestBuildReverseCmd_WithAudioReencodesBoth(t *testing.T) {
	want := []string{
		"-i", "in.mp4",
		"-filter_complex", "[0:v]reverse[v];[0:a:0]areverse[a0]",
		"-map", "[v]",
		"-map", "[a0]",
		"-c:v", "libx264",
		"-crf", "23",
		"-preset", "medium",
		"-pix_fmt", "yuv420p",
		"-c:a", "aac",
		"-b:a", "192k",
		"-y", "out.mp4",
	}
	cmd := BuildReverseCmd("in.mp4", true, 1, "out.mp4")
	if !reflect.DeepEqual(cmd.Args, want) {
		t.Fatalf("got  %#v\nwant %#v", cmd.Args, want)
	}
}

func TestBuildReverseCmd_MultiAudioKeepsEveryTrack(t *testing.T) {
	cmd := BuildReverseCmd("in.mkv", true, 3, "out.mkv")
	wantGraph := "[0:v]reverse[v];[0:a:0]areverse[a0];[0:a:1]areverse[a1];[0:a:2]areverse[a2]"
	if got := flagValue(cmd.Args, "-filter_complex"); got != wantGraph {
		t.Fatalf("filter_complex = %q, want %q", got, wantGraph)
	}
	var maps []string
	for i, a := range cmd.Args {
		if a == "-map" {
			maps = append(maps, cmd.Args[i+1])
		}
	}
	wantMaps := []string{"[v]", "[a0]", "[a1]", "[a2]"}
	if !reflect.DeepEqual(maps, wantMaps) {
		t.Fatalf("maps = %#v, want %#v", maps, wantMaps)
	}
}

func TestBuildReverseCmd_UnknownAudioCountFallsBackToSingleTrack(t *testing.T) {
	single := BuildReverseCmd("in.mp4", true, 1, "out.mp4")
	fallback := BuildReverseCmd("in.mp4", true, 0, "out.mp4")
	if !reflect.DeepEqual(single.Args, fallback.Args) {
		t.Fatalf("a failed probe must degrade to the single-track command:\ngot  %#v\nwant %#v",
			fallback.Args, single.Args)
	}
}

func TestBuildReverseCmd_WithoutAudioUsesVF(t *testing.T) {
	cmd := BuildReverseCmd("in.mp4", false, 0, "out.mp4")
	if got := flagValue(cmd.Args, "-vf"); got != "reverse" {
		t.Fatalf("-vf = %q, want reverse", got)
	}
	if got := flagValue(cmd.Args, "-c:v"); got != "libx264" {
		t.Fatalf("-c:v = %q, want libx264", got)
	}
	if got := flagValue(cmd.Args, "-filter_complex"); got != "" {
		t.Fatalf("video-only reverse must not use -filter_complex, got %q", got)
	}
}

// Reversing an HDR/10-bit source keeps the bit depth and signaling: the
// setparams segment composes after reverse in the same filtergraph.
func TestBuildReverseCmd_HDRSourceKeepsTenBit(t *testing.T) {
	want := []string{
		"-i", "in.mkv",
		"-filter_complex", "[0:v]reverse," + testHDRSuffix + "[v];[0:a:0]areverse[a0]",
		"-map", "[v]",
		"-map", "[a0]",
		"-c:v", "libx265",
		"-crf", "22",
		"-preset", "medium",
		"-x265-params", testHDRParams,
		"-c:a", "aac",
		"-b:a", "192k",
		"-y", "out.mkv",
	}
	cmd := BuildReverseCmd("in.mkv", true, 1, "out.mkv", testHDRProps)
	if !reflect.DeepEqual(cmd.Args, want) {
		t.Fatalf("got  %#v\nwant %#v", cmd.Args, want)
	}
}

// The video-only HDR reverse uses the -vf form like its SDR counterpart.
func TestBuildReverseCmd_HDRSourceWithoutAudioUsesVF(t *testing.T) {
	cmd := BuildReverseCmd("in.mkv", false, 0, "out.mkv", testHDRProps)
	if got, want := flagValue(cmd.Args, "-vf"), "reverse,"+testHDRSuffix; got != want {
		t.Fatalf("-vf = %q, want %q", got, want)
	}
	if got := flagValue(cmd.Args, "-c:v"); got != "libx265" {
		t.Fatalf("-c:v = %q, want libx265", got)
	}
	if got := flagValue(cmd.Args, "-c:a"); got != "copy" {
		t.Fatalf("-c:a = %q, want copy (no audio to re-encode)", got)
	}
	if got := flagValue(cmd.Args, "-filter_complex"); got != "" {
		t.Fatalf("video-only reverse must not use -filter_complex, got %q", got)
	}
}

func TestBuildMuteCmd_IsLossless(t *testing.T) {
	cmd := BuildMuteCmd("in.mp4", "out.mp4")
	foundAn, foundCopy, foundMaps := false, false, 0
	for i, a := range cmd.Args {
		if a == "-an" {
			foundAn = true
		}
		if a == "-c:v" && i+1 < len(cmd.Args) && cmd.Args[i+1] == "copy" {
			foundCopy = true
		}
		if a == "-map" {
			foundMaps++
		}
		if a == "libx264" {
			t.Fatalf("mute must not re-encode video: %#v", cmd.Args)
		}
	}
	if !foundAn || !foundCopy || foundMaps != 2 {
		t.Fatalf("want -an, -c:v copy and explicit video/subtitle maps, got %#v", cmd.Args)
	}
}

// A muted copy must keep every video and subtitle stream: plain -an with
// default stream selection retains only the "best" subtitle track.
func TestBuildMuteCmd_KeepsVideoAndSubtitleStreams(t *testing.T) {
	cmd := BuildMuteCmd("in.mkv", "out.mkv")
	joined := strings.Join(cmd.Args, " ")
	if !strings.Contains(joined, "-map 0:V?") || !strings.Contains(joined, "-map 0:s?") {
		t.Fatalf("mute must explicitly map video (V excludes cover art) and subtitles: %s", joined)
	}
	if !strings.Contains(joined, "-c:s copy") {
		t.Fatalf("subtitle streams must be stream-copied: %s", joined)
	}
}
