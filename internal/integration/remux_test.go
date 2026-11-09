//go:build integration

package integration

import (
	"path/filepath"
	"testing"
	"time"

	ffx "github.com/fsx8/ffwiz/internal/ffmpeg"
	ffprobe "github.com/fsx8/ffwiz/internal/ffprobe"
)

// Fixture layout of hdr4k.mkv (stable, see scripts/testmedia.sh):
// 0 hevc 4K HDR | 1 dts 5.1 eng | 2 eac3 5.1 deu | 3 aac eng
// 4 srt eng | 5 srt deu | 6 srt spa | 7 ass eng

func runRemux(t *testing.T, in, out string, actions map[int]ffx.TrackActionInfo) *ffprobe.ProbeResult {
	t.Helper()
	res := probeFile(t, in)
	cmd := ffx.BuildInteractiveConvertCmd(in, out, tracksFromProbe(res), actions)
	if cmd == nil {
		t.Fatal("expected a command")
	}
	runFFmpeg(t, 3*time.Minute, cmd.Args)
	return probeFile(t, out)
}

func removeActions(idxs ...int) map[int]ffx.TrackActionInfo {
	m := map[int]ffx.TrackActionInfo{}
	for _, i := range idxs {
		m[i] = ffx.TrackActionInfo{Action: ffx.ActionRemove}
	}
	return m
}

func codecNames(res *ffprobe.ProbeResult, typ string) []string {
	var out []string
	for _, s := range streamsOfType(res, typ) {
		out = append(out, s.CodecName)
	}
	return out
}

func tagValues(res *ffprobe.ProbeResult, typ, key string) []string {
	var out []string
	for _, s := range streamsOfType(res, typ) {
		out = append(out, s.Tags[key])
	}
	return out
}

func assertSeq(t *testing.T, what string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s = %v, want %v", what, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s = %v, want %v", what, got, want)
		}
	}
}

func TestRemuxPureCopyKeepsEveryStreamAndTag(t *testing.T) {
	requireTools(t)
	out := filepath.Join(t.TempDir(), "remux.mkv")
	res := runRemux(t, fx(t, "hdr4k.mkv"), out, nil)
	assertDuration(t, res, 4, 0.5)
	if got := len(res.Streams); got != 8 {
		t.Fatalf("pure remux must keep all 8 streams, got %d", got)
	}
	assertSeq(t, "video codecs", codecNames(res, "video"), []string{"hevc"})
	assertSeq(t, "audio codecs", codecNames(res, "audio"), []string{"dts", "eac3", "aac"})
	assertSeq(t, "audio langs", tagValues(res, "audio", "language"), []string{"eng", "deu", "eng"})
	assertSeq(t, "audio titles", tagValues(res, "audio", "title"), []string{"DTS 5.1", "EAC3 5.1", "Commentary"})
	assertSeq(t, "subtitle codecs", codecNames(res, "subtitle"), []string{"subrip", "subrip", "subrip", "ass"})
	assertSeq(t, "subtitle langs", tagValues(res, "subtitle", "language"), []string{"eng", "deu", "spa", "eng"})
}

func TestRemuxRemoveSingleAudioTrack(t *testing.T) {
	requireTools(t)
	res := runRemux(t, fx(t, "hdr4k.mkv"), filepath.Join(t.TempDir(), "out.mkv"), removeActions(1))
	assertSeq(t, "audio codecs", codecNames(res, "audio"), []string{"eac3", "aac"})
	assertSeq(t, "audio langs", tagValues(res, "audio", "language"), []string{"deu", "eng"})
	if got := len(streamsOfType(res, "video")); got != 1 {
		t.Fatalf("video must survive, got %d", got)
	}
	if got := len(streamsOfType(res, "subtitle")); got != 4 {
		t.Fatalf("subtitles must survive, got %d", got)
	}
}

func TestRemuxRemoveAllAudioTracks(t *testing.T) {
	requireTools(t)
	res := runRemux(t, fx(t, "hdr4k.mkv"), filepath.Join(t.TempDir(), "out.mkv"), removeActions(1, 2, 3))
	if got := len(streamsOfType(res, "audio")); got != 0 {
		t.Fatalf("all audio must be gone, got %d", got)
	}
	if v := firstStream(t, res, "video"); v.CodecName != "hevc" || v.PixFmt != "yuv420p10le" {
		t.Fatalf("4K HDR video must stay untouched, got %s %s", v.CodecName, v.PixFmt)
	}
	if got := len(streamsOfType(res, "subtitle")); got != 4 {
		t.Fatalf("subtitles must survive, got %d", got)
	}
}

func TestRemuxConvertAllAudioToAAC(t *testing.T) {
	requireTools(t)
	res := runRemux(t, fx(t, "hdr4k.mkv"), filepath.Join(t.TempDir(), "out.mkv"), map[int]ffx.TrackActionInfo{
		1: {Action: ffx.ActionConvert, Codec: "aac"},
		2: {Action: ffx.ActionConvert, Codec: "aac"},
		3: {Action: ffx.ActionConvert, Codec: "aac"},
	})
	audios := streamsOfType(res, "audio")
	if len(audios) != 3 {
		t.Fatalf("want 3 audio streams, got %d", len(audios))
	}
	for i, a := range audios {
		if a.CodecName != "aac" {
			t.Fatalf("audio %d = %s, want aac", i, a.CodecName)
		}
	}
	if audios[0].Channels != 6 || audios[1].Channels != 6 || audios[2].Channels != 2 {
		t.Fatalf("channel layouts must survive conversion: %d/%d/%d",
			audios[0].Channels, audios[1].Channels, audios[2].Channels)
	}
	assertSeq(t, "audio langs", tagValues(res, "audio", "language"), []string{"eng", "deu", "eng"})
}

// The bulk-convert shape: one codec choice applied to every audio track
// while the video copy and all four subtitles are kept explicitly.
func TestRemuxBulkConvertAllAudioKeepsSubtitles(t *testing.T) {
	requireTools(t)
	res := runRemux(t, fx(t, "hdr4k.mkv"), filepath.Join(t.TempDir(), "out.mkv"), map[int]ffx.TrackActionInfo{
		0: {Action: ffx.ActionKeep},
		1: {Action: ffx.ActionConvert, Codec: "aac"},
		2: {Action: ffx.ActionConvert, Codec: "aac"},
		3: {Action: ffx.ActionConvert, Codec: "aac"},
		4: {Action: ffx.ActionKeep},
		5: {Action: ffx.ActionKeep},
		6: {Action: ffx.ActionKeep},
		7: {Action: ffx.ActionKeep},
	})
	assertSeq(t, "video codecs", codecNames(res, "video"), []string{"hevc"})
	audios := streamsOfType(res, "audio")
	if len(audios) != 3 {
		t.Fatalf("want 3 audio streams, got %d", len(audios))
	}
	for i, a := range audios {
		if a.CodecName != "aac" {
			t.Fatalf("audio %d = %s, want aac", i, a.CodecName)
		}
	}
	assertSeq(t, "subtitle codecs", codecNames(res, "subtitle"), []string{"subrip", "subrip", "subrip", "ass"})
	assertSeq(t, "subtitle langs", tagValues(res, "subtitle", "language"), []string{"eng", "deu", "spa", "eng"})
	assertDuration(t, res, 4, 0.5)
}

func TestRemuxMixedAudioActions(t *testing.T) {
	requireTools(t)
	res := runRemux(t, fx(t, "hdr4k.mkv"), filepath.Join(t.TempDir(), "out.mkv"), map[int]ffx.TrackActionInfo{
		1: {Action: ffx.ActionConvert, Codec: "eac3"},
		2: {Action: ffx.ActionRemove},
		3: {Action: ffx.ActionKeep},
	})
	assertSeq(t, "audio codecs", codecNames(res, "audio"), []string{"eac3", "aac"})
	assertSeq(t, "audio langs", tagValues(res, "audio", "language"), []string{"eng", "eng"})
	if got := streamsOfType(res, "audio")[0].Channels; got != 6 {
		t.Fatalf("converted DTS 5.1 must keep 5.1, got %d channels", got)
	}
}

func TestRemuxAudioOnlyOutput(t *testing.T) {
	requireTools(t)
	res := runRemux(t, fx(t, "hdr4k.mkv"), filepath.Join(t.TempDir(), "out.mkv"),
		removeActions(0, 4, 5, 6, 7))
	if got := len(streamsOfType(res, "video")); got != 0 {
		t.Fatalf("video must be gone, got %d", got)
	}
	if got := len(streamsOfType(res, "subtitle")); got != 0 {
		t.Fatalf("subtitles must be gone, got %d", got)
	}
	assertSeq(t, "audio codecs", codecNames(res, "audio"), []string{"dts", "eac3", "aac"})
}

func TestRemuxKeepSomeSubtitles(t *testing.T) {
	requireTools(t)
	res := runRemux(t, fx(t, "hdr4k.mkv"), filepath.Join(t.TempDir(), "out.mkv"), removeActions(5, 6))
	subs := streamsOfType(res, "subtitle")
	if len(subs) != 2 {
		t.Fatalf("want 2 subtitle tracks, got %d", len(subs))
	}
	assertSeq(t, "subtitle codecs", codecNames(res, "subtitle"), []string{"subrip", "ass"})
	assertSeq(t, "subtitle langs", tagValues(res, "subtitle", "language"), []string{"eng", "eng"})
}

func TestRemuxRemoveAllSubtitles(t *testing.T) {
	requireTools(t)
	res := runRemux(t, fx(t, "hdr4k.mkv"), filepath.Join(t.TempDir(), "out.mkv"), removeActions(4, 5, 6, 7))
	if got := len(streamsOfType(res, "subtitle")); got != 0 {
		t.Fatalf("subtitles must be gone, got %d", got)
	}
	if got := len(streamsOfType(res, "audio")); got != 3 {
		t.Fatalf("audio must survive, got %d", got)
	}
}

func TestRemuxConvertSubtitleCodec(t *testing.T) {
	requireTools(t)
	res := runRemux(t, fx(t, "hdr4k.mkv"), filepath.Join(t.TempDir(), "out.mkv"),
		map[int]ffx.TrackActionInfo{4: {Action: ffx.ActionConvert, Codec: "ass"}})
	assertSeq(t, "subtitle codecs", codecNames(res, "subtitle"), []string{"ass", "subrip", "subrip", "ass"})
	assertSeq(t, "subtitle langs", tagValues(res, "subtitle", "language"), []string{"eng", "deu", "spa", "eng"})
}

// mp4-family output keeps the video and the muxable audio copies, quietly
// re-encodes DTS to AAC (DTS-in-MP4 fails on older ffmpeg builds, so
// "keep" is normalized to a compatible codec), and forces every subtitle
// to mov_text.
func TestRemuxToMp4AdaptsSubtitles(t *testing.T) {
	requireTools(t)
	res := runRemux(t, fx(t, "hdr4k.mkv"), filepath.Join(t.TempDir(), "out.mp4"), nil)
	if v := firstStream(t, res, "video"); v.CodecName != "hevc" {
		t.Fatalf("hevc must copy into mp4, got %s", v.CodecName)
	}
	assertSeq(t, "audio codecs", codecNames(res, "audio"), []string{"aac", "eac3", "aac"})
	assertSeq(t, "subtitle codecs", codecNames(res, "subtitle"), []string{"mov_text", "mov_text", "mov_text", "mov_text"})
	assertSeq(t, "subtitle langs", tagValues(res, "subtitle", "language"), []string{"eng", "deu", "spa", "eng"})
}

// webm cannot carry the source streams: video re-encodes to VP9, audio to
// Opus, and every subtitle is dropped — all via container normalization.
func TestRemuxToWebmConvertsAndDrops(t *testing.T) {
	requireTools(t)
	res := runRemux(t, fx(t, "subs.mkv"), filepath.Join(t.TempDir(), "out.webm"), nil)
	if v := firstStream(t, res, "video"); v.CodecName != "vp9" {
		t.Fatalf("video must auto-convert to vp9, got %s", v.CodecName)
	}
	assertSeq(t, "audio codecs", codecNames(res, "audio"), []string{"opus"})
	if got := len(streamsOfType(res, "subtitle")); got != 0 {
		t.Fatalf("webm cannot carry subtitles, got %d", got)
	}
}

func TestRemuxWebmToWebmStaysCopy(t *testing.T) {
	requireTools(t)
	res := runRemux(t, fx(t, "vp9.webm"), filepath.Join(t.TempDir(), "out.webm"), nil)
	assertSeq(t, "video codecs", codecNames(res, "video"), []string{"vp9"})
	assertSeq(t, "audio codecs", codecNames(res, "audio"), []string{"opus"})
	assertDuration(t, res, 4, 0.5)
}

func TestRemuxEverythingRemovedReturnsNil(t *testing.T) {
	requireTools(t)
	in := fx(t, "hdr4k.mkv")
	res := probeFile(t, in)
	cmd := ffx.BuildInteractiveConvertCmd(in, filepath.Join(t.TempDir(), "out.mkv"),
		tracksFromProbe(res), removeActions(0, 1, 2, 3, 4, 5, 6, 7))
	if cmd != nil {
		t.Fatalf("removing every track must yield no command, got %v", cmd.Args)
	}
}

func TestRemuxVideoConvertHevcToH264(t *testing.T) {
	requireTools(t)
	res := runRemux(t, fx(t, "hevc.mkv"), filepath.Join(t.TempDir(), "out.mkv"),
		map[int]ffx.TrackActionInfo{0: {Action: ffx.ActionConvert, Codec: "libx264"}})
	v := firstStream(t, res, "video")
	if v.CodecName != "h264" || v.Width != 640 || v.Height != 360 {
		t.Fatalf("video = %s %dx%d, want h264 640x360", v.CodecName, v.Width, v.Height)
	}
}
