package ffmpeg

import (
	"reflect"
	"testing"
)

func TestResizeOutputName(t *testing.T) {
	if got, want := ResizeOutputName("/a/b/movie.mkv", "720p"), "movie_720p.mkv"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	if got, want := ResizeOutputName("clip.mp4", "resized"), "clip_resized.mp4"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestBuildResizeCmd_Preset720pUsesAutoWidth(t *testing.T) {
	cmd := BuildResizeCmd("in.mp4", -2, 720, "out.mp4")
	want := []string{
		"-i", "in.mp4",
		"-vf", "scale=w=-2:h=720",
		"-c:v", "libx264",
		"-crf", "23",
		"-preset", "medium",
		"-pix_fmt", "yuv420p",
		"-c:a", "copy",
		"-y", "out.mp4",
	}
	if !reflect.DeepEqual(cmd.Args, want) {
		t.Fatalf("got %#v\nwant %#v", cmd.Args, want)
	}
}

func TestBuildResizeCmd_CustomWidthAutoHeight(t *testing.T) {
	cmd := BuildResizeCmd("in.mp4", 1920, -2, "out.mp4")
	if got, want := flagValue(cmd.Args, "-vf"), "scale=w=1920:h=-2"; got != want {
		t.Fatalf("-vf = %q, want %q", got, want)
	}
	if got := flagValue(cmd.Args, "-c:a"); got != "copy" {
		t.Fatalf("expected -c:a copy, got %q", got)
	}
	if got := flagValue(cmd.Args, "-crf"); got != "23" {
		t.Fatalf("expected -crf 23, got %q", got)
	}
}

func TestBuildResizeCmd_AutoDimensionsPassThrough(t *testing.T) {
	cmd := BuildResizeCmd("in.mp4", -2, -2, "out.mp4")
	if got, want := flagValue(cmd.Args, "-vf"), "scale=w=-2:h=-2"; got != want {
		t.Fatalf("-vf = %q, want %q (-2 must pass through to the scale filter)", got, want)
	}
}

// The HDR branch composes setparams after the scale filter; the -2
// auto-dimension logic keeps working unchanged.
func TestBuildResizeCmd_HDRComposesAfterScale(t *testing.T) {
	cmd := BuildResizeCmd("in.mkv", -2, 720, "out.mkv", testHDRProps)
	want := []string{
		"-i", "in.mkv",
		"-vf", "scale=w=-2:h=720," + testHDRSuffix,
		"-c:v", "libx265",
		"-crf", "22",
		"-preset", "medium",
		"-x265-params", testHDRParams,
		"-c:a", "copy",
		"-y", "out.mkv",
	}
	if !reflect.DeepEqual(cmd.Args, want) {
		t.Fatalf("got  %#v\nwant %#v", cmd.Args, want)
	}
}
