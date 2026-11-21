//go:build integration

package integration

import (
	"path/filepath"
	"testing"
	"time"

	ffx "github.com/fsx8/ffwiz/internal/ffmpeg"
	ffprobe "github.com/fsx8/ffwiz/internal/ffprobe"
)

// addedTracksFromProbe builds the external tracks the Modify Tracks "add"
// flow would import from a sidecar file: subtitle and audio streams only,
// each tagged with its source file and stream index.
func addedTracksFromProbe(t *testing.T, path string) []ffx.Track {
	t.Helper()
	res := probeFile(t, path)
	var tracks []ffx.Track
	for _, s := range res.Streams {
		switch s.CodecType {
		case "audio", "subtitle":
			tracks = append(tracks, ffx.Track{
				Index:       s.Index,
				Type:        ffx.TrackType(s.CodecType),
				Codec:       s.CodecName,
				SourceFile:  path,
				SourceIndex: s.Index,
			})
		}
	}
	if len(tracks) == 0 {
		t.Fatalf("no addable streams in %s", path)
	}
	return tracks
}

func runRemuxWithAdds(t *testing.T, out string, actions map[int]ffx.TrackActionInfo, adds ...string) *ffprobe.ProbeResult {
	t.Helper()
	in := fx(t, "hdr4k.mkv")
	res := probeFile(t, in)
	tracks := tracksFromProbe(res)
	for _, a := range adds {
		tracks = append(tracks, addedTracksFromProbe(t, a)...)
	}
	cmd := ffx.BuildInteractiveConvertCmd(in, out, tracks, actions)
	if cmd == nil {
		t.Fatal("expected a command")
	}
	runFFmpeg(t, 3*time.Minute, cmd.Args)
	return probeFile(t, out)
}

// Merging a downloaded .srt into a movie is the motivating case: the
// remux keeps every original stream and appends the external subtitle as
// a fifth subtitle track.
func TestRemuxAddExternalSubtitle(t *testing.T) {
	requireTools(t)
	out := filepath.Join(t.TempDir(), "with_subs.mkv")
	res := runRemuxWithAdds(t, out, nil, fx(t, "hdr4k_en.srt"))
	assertDuration(t, res, 4, 0.5)
	if got := len(res.Streams); got != 9 {
		t.Fatalf("8 original streams + 1 added, got %d", got)
	}
	if v := firstStream(t, res, "video"); v.CodecName != "hevc" {
		t.Fatalf("video must stay a copy, got %s", v.CodecName)
	}
	assertSeq(t, "audio codecs", codecNames(res, "audio"), []string{"dts", "eac3", "aac"})
	// The added srt is mapped last in the subtitle group; a bare sidecar
	// file carries no language tag.
	assertSeq(t, "subtitle codecs", codecNames(res, "subtitle"), []string{"subrip", "subrip", "subrip", "ass", "subrip"})
	assertSeq(t, "subtitle langs", tagValues(res, "subtitle", "language"), []string{"eng", "deu", "spa", "eng", ""})
}

// The same merge into an mp4: the added srt cannot copy into mp4, so the
// container normalization converts it to mov_text like every other text
// subtitle — and all nine streams survive.
func TestRemuxAddExternalSubtitleToMp4BecomesMovText(t *testing.T) {
	requireTools(t)
	out := filepath.Join(t.TempDir(), "with_subs.mp4")
	res := runRemuxWithAdds(t, out, nil, fx(t, "hdr4k_en.srt"))
	if got := len(res.Streams); got != 9 {
		t.Fatalf("all streams must arrive in mp4, got %d", got)
	}
	if v := firstStream(t, res, "video"); v.CodecName != "hevc" {
		t.Fatalf("hevc must copy into mp4, got %s", v.CodecName)
	}
	assertSeq(t, "audio codecs", codecNames(res, "audio"), []string{"aac", "eac3", "aac"})
	subs := streamsOfType(res, "subtitle")
	if len(subs) != 5 {
		t.Fatalf("want 5 subtitle tracks, got %d", len(subs))
	}
	for i, s := range subs {
		if s.CodecName != "mov_text" {
			t.Fatalf("subtitle %d = %s, want mov_text (mp4 normalization)", i, s.CodecName)
		}
	}
}

// An external audio file is imported the same way: mp3 copies into mkv as
// a fourth audio track next to the untouched originals. The 6s mp3
// outlasts the 4s video, so the container duration follows the longest
// stream.
func TestRemuxAddExternalAudio(t *testing.T) {
	requireTools(t)
	out := filepath.Join(t.TempDir(), "with_audio.mkv")
	res := runRemuxWithAdds(t, out, nil, fx(t, "audio_only.mp3"))
	assertSeq(t, "audio codecs", codecNames(res, "audio"), []string{"dts", "eac3", "aac", "mp3"})
	if got := len(streamsOfType(res, "subtitle")); got != 4 {
		t.Fatalf("subtitles must survive untouched, got %d", got)
	}
	assertDuration(t, res, 6, 0.5)
}
