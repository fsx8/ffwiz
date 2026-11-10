package ffmpeg

import (
	"testing"
)

func maps(args []string) []string {
	var out []string
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "-map" {
			out = append(out, args[i+1])
		}
	}
	return out
}

func flagValue(args []string, flag string) string {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == flag {
			return args[i+1]
		}
	}
	return ""
}

func TestInteractiveConvert_KeepAllTracksWithGappedIndexes(t *testing.T) {
	tracks := []Track{
		{Index: 0, Type: TrackVideo, Codec: "h264"},
		{Index: 2, Type: TrackAudio, Codec: "aac"},
		{Index: 5, Type: TrackSubtitle, Codec: "subrip"},
	}
	cmd := BuildInteractiveConvertCmd("input.mkv", "output.mkv", tracks, map[int]TrackActionInfo{})
	if cmd == nil {
		t.Fatal("expected cmd")
	}
	if got, want := maps(cmd.Args), []string{"0:0", "0:2", "0:5"}; !equalStrings(got, want) {
		t.Fatalf("maps mismatch\ngot:  %#v\nwant: %#v", got, want)
	}
	if got := flagValue(cmd.Args, "-c:v:0"); got != "copy" {
		t.Fatalf("expected -c:v:0 copy, got %q", got)
	}
	if got := flagValue(cmd.Args, "-c:a:0"); got != "copy" {
		t.Fatalf("expected -c:a:0 copy, got %q", got)
	}
	if got := flagValue(cmd.Args, "-c:s:0"); got != "copy" {
		t.Fatalf("expected -c:s:0 copy, got %q", got)
	}
}

func TestInteractiveConvert_AllTracksRemovedReturnsNil(t *testing.T) {
	tracks := []Track{
		{Index: 0, Type: TrackVideo, Codec: "h264"},
		{Index: 2, Type: TrackAudio, Codec: "aac"},
		{Index: 5, Type: TrackSubtitle, Codec: "subrip"},
	}
	actions := map[int]TrackActionInfo{
		0: {Action: ActionRemove},
		1: {Action: ActionRemove},
		2: {Action: ActionRemove},
	}
	if cmd := BuildInteractiveConvertCmd("input.mkv", "output.mkv", tracks, actions); cmd != nil {
		t.Fatalf("expected nil cmd, got %+v", cmd)
	}
}

func TestInteractiveConvert_RemoveVideoOnlyOutputsAudioAndSubs(t *testing.T) {
	tracks := []Track{
		{Index: 0, Type: TrackVideo, Codec: "h264"},
		{Index: 2, Type: TrackAudio, Codec: "aac"},
		{Index: 5, Type: TrackSubtitle, Codec: "subrip"},
	}
	actions := map[int]TrackActionInfo{0: {Action: ActionRemove}}
	cmd := BuildInteractiveConvertCmd("input.mkv", "output.mkv", tracks, actions)
	if cmd == nil {
		t.Fatal("expected cmd")
	}
	if got, want := maps(cmd.Args), []string{"0:2", "0:5"}; !equalStrings(got, want) {
		t.Fatalf("maps mismatch\ngot:  %#v\nwant: %#v", got, want)
	}
	if got := flagValue(cmd.Args, "-c:v:0"); got != "" {
		t.Fatalf("expected no -c:v:0, got %q", got)
	}
	if got := flagValue(cmd.Args, "-c:a:0"); got != "copy" {
		t.Fatalf("expected -c:a:0 copy, got %q", got)
	}
	if got := flagValue(cmd.Args, "-c:s:0"); got != "copy" {
		t.Fatalf("expected -c:s:0 copy, got %q", got)
	}
}

func TestInteractiveConvert_RemoveAudioTrackByTrackID(t *testing.T) {
	tracks := []Track{
		{Index: 0, Type: TrackVideo, Codec: "h264"},
		{Index: 2, Type: TrackAudio, Codec: "aac"},
		{Index: 5, Type: TrackSubtitle, Codec: "subrip"},
	}
	actions := map[int]TrackActionInfo{1: {Action: ActionRemove}}
	cmd := BuildInteractiveConvertCmd("input.mkv", "output.mkv", tracks, actions)
	if cmd == nil {
		t.Fatal("expected cmd")
	}
	if got, want := maps(cmd.Args), []string{"0:0", "0:5"}; !equalStrings(got, want) {
		t.Fatalf("maps mismatch\ngot:  %#v\nwant: %#v", got, want)
	}
	if got := flagValue(cmd.Args, "-c:a:0"); got != "" {
		t.Fatalf("expected no -c:a:0, got %q", got)
	}
}

func TestInteractiveConvert_ConvertAudioSetsCodecAndBitrate(t *testing.T) {
	tracks := []Track{
		{Index: 0, Type: TrackVideo, Codec: "h264"},
		{Index: 2, Type: TrackAudio, Codec: "aac"},
		{Index: 5, Type: TrackSubtitle, Codec: "subrip"},
	}
	actions := map[int]TrackActionInfo{1: {Action: ActionConvert, Codec: "aac"}}
	cmd := BuildInteractiveConvertCmd("input.mkv", "output.mkv", tracks, actions)
	if cmd == nil {
		t.Fatal("expected cmd")
	}
	if got := flagValue(cmd.Args, "-c:a:0"); got != "aac" {
		t.Fatalf("expected -c:a:0 aac, got %q", got)
	}
	if got := flagValue(cmd.Args, "-b:a:0"); got != "192k" {
		t.Fatalf("expected -b:a:0 192k, got %q", got)
	}
}

func TestInteractiveConvert_ConvertAudioFromUIChoiceIsNormalized(t *testing.T) {
	tracks := []Track{
		{Index: 0, Type: TrackVideo, Codec: "h264"},
		{Index: 2, Type: TrackAudio, Codec: "aac"},
		{Index: 5, Type: TrackSubtitle, Codec: "subrip"},
	}
	actions := map[int]TrackActionInfo{1: {Action: ActionConvert, Codec: "libmp3lame (MP3)"}}
	cmd := BuildInteractiveConvertCmd("input.mkv", "output.mkv", tracks, actions)
	if cmd == nil {
		t.Fatal("expected cmd")
	}
	if got := flagValue(cmd.Args, "-c:a:0"); got != "libmp3lame" {
		t.Fatalf("expected libmp3lame, got %q", got)
	}
	if got := flagValue(cmd.Args, "-b:a:0"); got != "192k" {
		t.Fatalf("expected 192k, got %q", got)
	}
}

func TestInteractiveConvert_MultipleAudioKeepAndConvertIndexesCompact(t *testing.T) {
	tracks := []Track{
		{Index: 0, Type: TrackVideo, Codec: "h264"},
		{Index: 1, Type: TrackAudio, Codec: "aac"},
		{Index: 4, Type: TrackAudio, Codec: "dts"},
		{Index: 6, Type: TrackSubtitle, Codec: "subrip"},
	}
	actions := map[int]TrackActionInfo{2: {Action: ActionConvert, Codec: "libopus (Opus)"}}
	cmd := BuildInteractiveConvertCmd("input.mkv", "output.mkv", tracks, actions)
	if cmd == nil {
		t.Fatal("expected cmd")
	}
	if got, want := maps(cmd.Args), []string{"0:0", "0:1", "0:4", "0:6"}; !equalStrings(got, want) {
		t.Fatalf("maps mismatch\ngot:  %#v\nwant: %#v", got, want)
	}
	if got := flagValue(cmd.Args, "-c:a:0"); got != "copy" {
		t.Fatalf("expected audio0 copy, got %q", got)
	}
	if got := flagValue(cmd.Args, "-c:a:1"); got != "libopus" {
		t.Fatalf("expected audio1 libopus, got %q", got)
	}
	if got := flagValue(cmd.Args, "-b:a:1"); got != "160k" {
		t.Fatalf("expected audio1 160k, got %q", got)
	}
}

func TestInteractiveConvert_ConvertSubtitleChoiceIsNormalized(t *testing.T) {
	tracks := []Track{
		{Index: 0, Type: TrackVideo, Codec: "h264"},
		{Index: 2, Type: TrackAudio, Codec: "aac"},
		{Index: 5, Type: TrackSubtitle, Codec: "subrip"},
	}
	actions := map[int]TrackActionInfo{2: {Action: ActionConvert, Codec: "srt (SubRip)"}}
	cmd := BuildInteractiveConvertCmd("input.mkv", "output.mkv", tracks, actions)
	if cmd == nil {
		t.Fatal("expected cmd")
	}
	if got := flagValue(cmd.Args, "-c:s:0"); got != "srt" {
		t.Fatalf("expected srt, got %q", got)
	}
}

func TestInteractiveConvert_ConvertSubtitleMovTextChoiceIsNormalized(t *testing.T) {
	tracks := []Track{
		{Index: 0, Type: TrackVideo, Codec: "h264"},
		{Index: 2, Type: TrackAudio, Codec: "aac"},
		{Index: 5, Type: TrackSubtitle, Codec: "subrip"},
	}
	actions := map[int]TrackActionInfo{2: {Action: ActionConvert, Codec: "mov_text (MP4)"}}
	cmd := BuildInteractiveConvertCmd("input.mp4", "output.mp4", tracks, actions)
	if cmd == nil {
		t.Fatal("expected cmd")
	}
	if got := flagValue(cmd.Args, "-c:s:0"); got != "mov_text" {
		t.Fatalf("expected mov_text, got %q", got)
	}
}

func TestInteractiveConvert_MOVTextInMkvBecomesSRT(t *testing.T) {
	tracks := []Track{
		{Index: 0, Type: TrackVideo, Codec: "h264"},
		{Index: 2, Type: TrackAudio, Codec: "aac"},
		{Index: 5, Type: TrackSubtitle, Codec: "subrip"},
	}
	actions := map[int]TrackActionInfo{2: {Action: ActionConvert, Codec: "mov_text (MP4)"}}
	cmd := BuildInteractiveConvertCmd("input.mp4", "output.mkv", tracks, actions)
	if cmd == nil {
		t.Fatal("expected cmd")
	}
	if got := flagValue(cmd.Args, "-c:s:0"); got != "srt" {
		t.Fatalf("mkv cannot hold mov_text; expected srt, got %q", got)
	}
}

func TestInteractiveConvert_SubtitleCopyIntoMP4BecomesMOVText(t *testing.T) {
	tracks := []Track{
		{Index: 0, Type: TrackVideo, Codec: "h264"},
		{Index: 5, Type: TrackSubtitle, Codec: "subrip"},
	}
	cmd := BuildInteractiveConvertCmd("input.mkv", "output.mp4", tracks, nil)
	if cmd == nil {
		t.Fatal("expected cmd")
	}
	if got := flagValue(cmd.Args, "-c:s:0"); got != "mov_text" {
		t.Fatalf("mp4 cannot hold srt streams; expected mov_text, got %q", got)
	}
}

func TestInteractiveConvert_WebmReEncodesH264Copy(t *testing.T) {
	tracks := []Track{
		{Index: 0, Type: TrackVideo, Codec: "h264"},
		{Index: 2, Type: TrackAudio, Codec: "aac"},
	}
	cmd := BuildInteractiveConvertCmd("input.mp4", "output.webm", tracks, nil)
	if cmd == nil {
		t.Fatal("expected cmd")
	}
	if got := flagValue(cmd.Args, "-c:v:0"); got != "libvpx-vp9" {
		t.Fatalf("webm cannot hold H.264; expected libvpx-vp9, got %q", got)
	}
	if got := flagValue(cmd.Args, "-c:a:0"); got != "libopus" {
		t.Fatalf("webm cannot hold AAC; expected libopus, got %q", got)
	}
}

func TestInteractiveConvert_WebmKeepsVP9Copy(t *testing.T) {
	tracks := []Track{
		{Index: 0, Type: TrackVideo, Codec: "vp9"},
		{Index: 2, Type: TrackAudio, Codec: "opus"},
	}
	cmd := BuildInteractiveConvertCmd("input.webm", "output.webm", tracks, nil)
	if cmd == nil {
		t.Fatal("expected cmd")
	}
	if got := flagValue(cmd.Args, "-c:v:0"); got != "copy" {
		t.Fatalf("expected copy for VP9 source, got %q", got)
	}
	if got := flagValue(cmd.Args, "-c:a:0"); got != "copy" {
		t.Fatalf("expected copy for opus source, got %q", got)
	}
}

func TestInteractiveConvert_WebmDropsSubtitles(t *testing.T) {
	tracks := []Track{
		{Index: 0, Type: TrackVideo, Codec: "h264"},
		{Index: 2, Type: TrackSubtitle, Codec: "subrip"},
	}
	cmd := BuildInteractiveConvertCmd("input.mkv", "output.webm", tracks, nil)
	if cmd == nil {
		t.Fatal("expected cmd (video kept)")
	}
	if got, want := maps(cmd.Args), []string{"0:0"}; !equalStrings(got, want) {
		t.Fatalf("webm cannot carry subtitles; maps: got %#v want %#v", got, want)
	}
}

// A bitmap subtitle (PGS) cannot be converted to mov_text — ffmpeg only
// converts text to text — so it must be dropped from the mp4 output
// entirely instead of producing a command that fails at the encoder.
func TestInteractiveConvert_PgsSubtitleIntoMP4IsDropped(t *testing.T) {
	tracks := []Track{
		{Index: 0, Type: TrackVideo, Codec: "h264"},
		{Index: 2, Type: TrackAudio, Codec: "aac"},
		{Index: 3, Type: TrackSubtitle, Codec: "hdmv_pgs_subtitle"},
	}
	cmd := BuildInteractiveConvertCmd("input.mkv", "output.mp4", tracks, nil)
	if cmd == nil {
		t.Fatal("expected cmd (video and audio kept)")
	}
	if got, want := maps(cmd.Args), []string{"0:0", "0:2"}; !equalStrings(got, want) {
		t.Fatalf("a bitmap subtitle cannot convert to mov_text; maps: got %#v want %#v", got, want)
	}
	if got := flagValue(cmd.Args, "-c:s:0"); got != "" {
		t.Fatalf("expected no -c:s:0, got %q", got)
	}
}

func TestInteractiveConvert_ConvertFlacSetsNoBitrate(t *testing.T) {
	tracks := []Track{
		{Index: 0, Type: TrackVideo, Codec: "h264"},
		{Index: 2, Type: TrackAudio, Codec: "aac"},
	}
	actions := map[int]TrackActionInfo{1: {Action: ActionConvert, Codec: "flac (FLAC)"}}
	cmd := BuildInteractiveConvertCmd("input.mkv", "output.mkv", tracks, actions)
	if cmd == nil {
		t.Fatal("expected cmd")
	}
	if got := flagValue(cmd.Args, "-c:a:0"); got != "flac" {
		t.Fatalf("expected native flac encoder, got %q", got)
	}
	if got := flagValue(cmd.Args, "-b:a:0"); got != "" {
		t.Fatalf("flac is lossless; did not expect a bitrate, got %q", got)
	}
}

func TestInteractiveConvert_ConvertSubtitleLegacyChoiceIsNormalized(t *testing.T) {
	tracks := []Track{
		{Index: 0, Type: TrackVideo, Codec: "h264"},
		{Index: 2, Type: TrackAudio, Codec: "aac"},
		{Index: 5, Type: TrackSubtitle, Codec: "subrip"},
	}
	actions := map[int]TrackActionInfo{2: {Action: ActionConvert, Codec: "(SubRip)"}}
	cmd := BuildInteractiveConvertCmd("input.mkv", "output.mkv", tracks, actions)
	if cmd == nil {
		t.Fatal("expected cmd")
	}
	if got := flagValue(cmd.Args, "-c:s:0"); got != "srt" {
		t.Fatalf("expected srt, got %q", got)
	}
}

func TestInteractiveConvert_MultipleSubtitlesRemoveThenConvertIndexesCompact(t *testing.T) {
	tracks := []Track{
		{Index: 0, Type: TrackVideo, Codec: "h264"},
		{Index: 2, Type: TrackAudio, Codec: "aac"},
		{Index: 5, Type: TrackSubtitle, Codec: "subrip"},
		{Index: 7, Type: TrackSubtitle, Codec: "ass"},
	}
	actions := map[int]TrackActionInfo{
		2: {Action: ActionRemove},
		3: {Action: ActionConvert, Codec: "srt (SubRip)"},
	}
	cmd := BuildInteractiveConvertCmd("input.mkv", "output.mkv", tracks, actions)
	if cmd == nil {
		t.Fatal("expected cmd")
	}
	if got, want := maps(cmd.Args), []string{"0:0", "0:2", "0:7"}; !equalStrings(got, want) {
		t.Fatalf("maps mismatch\ngot:  %#v\nwant: %#v", got, want)
	}
	if got := flagValue(cmd.Args, "-c:s:0"); got != "srt" {
		t.Fatalf("expected srt, got %q", got)
	}
	if got := flagValue(cmd.Args, "-c:s:1"); got != "" {
		t.Fatalf("expected no -c:s:1, got %q", got)
	}
}

func TestInteractiveConvert_MapOrderIsVideoThenAudioThenSubtitle(t *testing.T) {
	tracks := []Track{
		{Index: 2, Type: TrackAudio, Codec: "aac"},
		{Index: 5, Type: TrackSubtitle, Codec: "subrip"},
		{Index: 0, Type: TrackVideo, Codec: "h264"},
	}
	cmd := BuildInteractiveConvertCmd("input.mkv", "output.mkv", tracks, map[int]TrackActionInfo{})
	if cmd == nil {
		t.Fatal("expected cmd")
	}
	if got, want := maps(cmd.Args), []string{"0:0", "0:2", "0:5"}; !equalStrings(got, want) {
		t.Fatalf("maps mismatch\ngot:  %#v\nwant: %#v", got, want)
	}
}

func TestInteractiveConvert_ConvertVideoSetsExpectedArgs(t *testing.T) {
	tracks := []Track{
		{Index: 0, Type: TrackVideo, Codec: "h264"},
		{Index: 2, Type: TrackAudio, Codec: "aac"},
		{Index: 5, Type: TrackSubtitle, Codec: "subrip"},
	}
	actions := map[int]TrackActionInfo{0: {Action: ActionConvert, Codec: "libx265 (H.265/HEVC)"}}
	cmd := BuildInteractiveConvertCmd("input.mkv", "output.mkv", tracks, actions)
	if cmd == nil {
		t.Fatal("expected cmd")
	}
	if got := flagValue(cmd.Args, "-c:v:0"); got != "libx265" {
		t.Fatalf("expected libx265, got %q", got)
	}
	if got := flagValue(cmd.Args, "-crf:v:0"); got != "28" {
		t.Fatalf("expected crf 28, got %q", got)
	}
	if got := flagValue(cmd.Args, "-preset:v:0"); got != "medium" {
		t.Fatalf("expected preset medium, got %q", got)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// The VP8/AV1 choices must carry rate-control arguments: without them
// libvpx falls back to a 200k CBR and libaom to extremely slow defaults.
func TestInteractiveConvert_VP8ChoiceGetsRateControl(t *testing.T) {
	tracks := []Track{{Index: 0, Type: TrackVideo, Codec: "h264"}}
	cmd := BuildInteractiveConvertCmd("in.mkv", "out.mkv", tracks,
		map[int]TrackActionInfo{0: {Action: ActionConvert, Codec: "libvpx (VP8)"}})
	if cmd == nil {
		t.Fatal("expected cmd")
	}
	if got := flagValue(cmd.Args, "-c:v:0"); got != "libvpx" {
		t.Fatalf("codec = %q, want libvpx", got)
	}
	if got := flagValue(cmd.Args, "-crf:v:0"); got == "" {
		t.Fatal("VP8 conversion must set -crf (constrained quality)")
	}
	if got := flagValue(cmd.Args, "-b:v:0"); got == "" {
		t.Fatal("VP8 conversion must set a bitrate ceiling")
	}
}

func TestInteractiveConvert_AV1ChoiceGetsRateControl(t *testing.T) {
	tracks := []Track{{Index: 0, Type: TrackVideo, Codec: "h264"}}
	cmd := BuildInteractiveConvertCmd("in.mkv", "out.mkv", tracks,
		map[int]TrackActionInfo{0: {Action: ActionConvert, Codec: "libaom-av1 (AV1)"}})
	if cmd == nil {
		t.Fatal("expected cmd")
	}
	if got := flagValue(cmd.Args, "-crf:v:0"); got == "" {
		t.Fatal("AV1 conversion must set -crf")
	}
	if got := flagValue(cmd.Args, "-b:v:0"); got != "0" {
		t.Fatalf("AV1 conversion must use constant quality (-b:v 0), got %q", got)
	}
}

// HEVC landing in the MP4 family is tagged hvc1 so Apple devices recognize
// it; copies of HEVC sources and x265 conversions both qualify.
func TestInteractiveConvert_HevcCopyIntoMP4GetsHvc1Tag(t *testing.T) {
	tracks := []Track{{Index: 0, Type: TrackVideo, Codec: "hevc"}}
	cmd := BuildInteractiveConvertCmd("in.mkv", "out.mp4", tracks, nil)
	if cmd == nil {
		t.Fatal("expected cmd")
	}
	if got := flagValue(cmd.Args, "-c:v:0"); got != "copy" {
		t.Fatalf("HEVC copy into mp4 must stay a copy, got %q", got)
	}
	if got := flagValue(cmd.Args, "-tag:v:0"); got != "hvc1" {
		t.Fatalf("tag = %q, want hvc1", got)
	}
}

func TestInteractiveConvert_HevcIntoMkvGetsNoTag(t *testing.T) {
	tracks := []Track{{Index: 0, Type: TrackVideo, Codec: "hevc"}}
	cmd := BuildInteractiveConvertCmd("in.mkv", "out.mkv", tracks, nil)
	if cmd == nil {
		t.Fatal("expected cmd")
	}
	if got := flagValue(cmd.Args, "-tag:v:0"); got != "" {
		t.Fatalf("mkv output must not get an hvc1 tag, got %q", got)
	}
}

func TestInteractiveConvert_H264IntoMP4GetsNoTag(t *testing.T) {
	tracks := []Track{{Index: 0, Type: TrackVideo, Codec: "h264"}}
	cmd := BuildInteractiveConvertCmd("in.mkv", "out.mp4", tracks, nil)
	if cmd == nil {
		t.Fatal("expected cmd")
	}
	if got := flagValue(cmd.Args, "-tag:v:0"); got != "" {
		t.Fatalf("H.264 must not be tagged hvc1, got %q", got)
	}
}

func TestInteractiveConvert_X265ConversionIntoMP4GetsHvc1Tag(t *testing.T) {
	tracks := []Track{{Index: 0, Type: TrackVideo, Codec: "h264"}}
	cmd := BuildInteractiveConvertCmd("in.mkv", "out.mp4", tracks,
		map[int]TrackActionInfo{0: {Action: ActionConvert, Codec: "libx265 (H.265/HEVC)"}})
	if cmd == nil {
		t.Fatal("expected cmd")
	}
	if got := flagValue(cmd.Args, "-c:v:0"); got != "libx265" {
		t.Fatalf("codec = %q, want libx265", got)
	}
	if got := flagValue(cmd.Args, "-tag:v:0"); got != "hvc1" {
		t.Fatalf("tag = %q, want hvc1", got)
	}
}

// Converting an HDR/10-bit video track to libx265 keeps bit depth and
// signaling: the per-output-index filter lays in the setparams side data
// and the metadata rides along as encoder parameters.
func TestInteractiveConvert_X265OnHDRGetsTenBitBranch(t *testing.T) {
	tracks := []Track{
		{Index: 0, Type: TrackVideo, Codec: "hevc", Color: testHDRProps},
		{Index: 2, Type: TrackAudio, Codec: "aac"},
	}
	cmd := BuildInteractiveConvertCmd("in.mkv", "out.mkv", tracks,
		map[int]TrackActionInfo{0: {Action: ActionConvert, Codec: "libx265 (H.265/HEVC)"}})
	if cmd == nil {
		t.Fatal("expected cmd")
	}
	if got, want := flagValue(cmd.Args, "-filter:v:0"), testHDRSuffix; got != want {
		t.Fatalf("-filter:v:0 = %q, want %q", got, want)
	}
	if got, want := flagValue(cmd.Args, "-x265-params:v:0"), testHDRParams; got != want {
		t.Fatalf("-x265-params:v:0 = %q, want %q", got, want)
	}
	if got := flagValue(cmd.Args, "-crf:v:0"); got != "28" {
		t.Fatalf("-crf:v:0 = %q, want 28 (unchanged quality choice)", got)
	}
	if got := flagValue(cmd.Args, "-pix_fmt:v:0"); got != "" {
		t.Fatalf("the HDR branch must not force -pix_fmt, got %q", got)
	}
}

// An SDR source converts to libx265 exactly as before: no filter, no
// encoder metadata parameters.
func TestInteractiveConvert_X265OnSDRStaysUnchanged(t *testing.T) {
	tracks := []Track{{Index: 0, Type: TrackVideo, Codec: "h264"}}
	cmd := BuildInteractiveConvertCmd("in.mkv", "out.mkv", tracks,
		map[int]TrackActionInfo{0: {Action: ActionConvert, Codec: "libx265 (H.265/HEVC)"}})
	if cmd == nil {
		t.Fatal("expected cmd")
	}
	if got := flagValue(cmd.Args, "-filter:v:0"); got != "" {
		t.Fatalf("SDR x265 conversion must not filter, got -filter:v:0 %q", got)
	}
	if got := flagValue(cmd.Args, "-x265-params:v:0"); got != "" {
		t.Fatalf("SDR x265 conversion must not emit -x265-params, got %q", got)
	}
}

// The crushing codec choice keeps its 8-bit pipeline even on an HDR
// source: only libx265 gets the preserving branch.
func TestInteractiveConvert_X264OnHDRStaysEightBit(t *testing.T) {
	tracks := []Track{{Index: 0, Type: TrackVideo, Codec: "hevc", Color: testHDRProps}}
	cmd := BuildInteractiveConvertCmd("in.mkv", "out.mkv", tracks,
		map[int]TrackActionInfo{0: {Action: ActionConvert, Codec: "libx264 (H.264)"}})
	if cmd == nil {
		t.Fatal("expected cmd")
	}
	if got, want := flagValue(cmd.Args, "-pix_fmt:v:0"), "yuv420p"; got != want {
		t.Fatalf("-pix_fmt:v:0 = %q, want %q (x264 choice stays 8-bit)", got, want)
	}
	if got := flagValue(cmd.Args, "-filter:v:0"); got != "" {
		t.Fatalf("x264 conversion must not filter, got -filter:v:0 %q", got)
	}
}
