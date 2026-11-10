package ffmpeg

import (
	"strings"
	"testing"
)

func TestBuildRotateCmd_90(t *testing.T) {
	cmd := BuildRotateCmd("in.mp4", 90, "out.mp4")
	want := []string{
		"-i", "in.mp4",
		"-vf", "transpose=1",
		"-c:v", "libx264",
		"-crf", "23",
		"-preset", "medium",
		"-pix_fmt", "yuv420p",
		"-c:a", "copy",
		"-y", "out.mp4",
	}
	if got := cmd.Args; !equalArgs(got, want) {
		t.Fatalf("got %#v want %#v", got, want)
	}
}

func TestBuildRotateCmd_180(t *testing.T) {
	cmd := BuildRotateCmd("in.mp4", 180, "out.mp4")
	want := []string{
		"-i", "in.mp4",
		"-vf", "hflip,vflip",
		"-c:v", "libx264",
		"-crf", "23",
		"-preset", "medium",
		"-pix_fmt", "yuv420p",
		"-c:a", "copy",
		"-y", "out.mp4",
	}
	if got := cmd.Args; !equalArgs(got, want) {
		t.Fatalf("got %#v want %#v", got, want)
	}
}

func TestBuildRotateCmd_270(t *testing.T) {
	cmd := BuildRotateCmd("in.mp4", 270, "out.mp4")
	if got := flagValue(cmd.Args, "-vf"); got != "transpose=2" {
		t.Fatalf("-vf = %q, want transpose=2", got)
	}
}

func TestBuildRotateCmd_InvalidDegreesYieldZeroCmd(t *testing.T) {
	for _, degrees := range []int{0, 45, -90, 360} {
		if cmd := BuildRotateCmd("in.mp4", degrees, "out.mp4"); len(cmd.Args) != 0 {
			t.Fatalf("degrees %d must yield a zero Cmd, got %#v", degrees, cmd.Args)
		}
	}
}

func TestBuildFlipCmd_Horizontal(t *testing.T) {
	cmd := BuildFlipCmd("in.mp4", "h", "out.mp4")
	want := []string{
		"-i", "in.mp4",
		"-vf", "hflip",
		"-c:v", "libx264",
		"-crf", "23",
		"-preset", "medium",
		"-pix_fmt", "yuv420p",
		"-c:a", "copy",
		"-y", "out.mp4",
	}
	if got := cmd.Args; !equalArgs(got, want) {
		t.Fatalf("got %#v want %#v", got, want)
	}
}

func TestBuildFlipCmd_Vertical(t *testing.T) {
	cmd := BuildFlipCmd("in.mp4", "v", "out.mp4")
	if got := flagValue(cmd.Args, "-vf"); got != "vflip" {
		t.Fatalf("-vf = %q, want vflip", got)
	}
}

func TestBuildFlipCmd_InvalidDirectionYieldsZeroCmd(t *testing.T) {
	for _, dir := range []string{"", "x", "H", "horizontal"} {
		if cmd := BuildFlipCmd("in.mp4", dir, "out.mp4"); len(cmd.Args) != 0 {
			t.Fatalf("direction %q must yield a zero Cmd, got %#v", dir, cmd.Args)
		}
	}
}

func TestBuildCropCmd(t *testing.T) {
	cmd := BuildCropCmd("in.mp4", 160, 120, 320, 240, "out.mp4")
	want := []string{
		"-i", "in.mp4",
		"-vf", "crop=w=320:h=240:x=160:y=120",
		"-c:v", "libx264",
		"-crf", "23",
		"-preset", "medium",
		"-pix_fmt", "yuv420p",
		"-c:a", "copy",
		"-y", "out.mp4",
	}
	if got := cmd.Args; !equalArgs(got, want) {
		t.Fatalf("got %#v want %#v", got, want)
	}
}

func TestBuildCropCmd_NonPositiveValuesYieldZeroCmd(t *testing.T) {
	cases := []struct{ x, y, w, h int }{
		{-1, 120, 320, 240},
		{160, -1, 320, 240},
		{160, 120, 0, 240},
		{160, 120, 320, -240},
	}
	for _, c := range cases {
		cmd := BuildCropCmd("in.mp4", c.x, c.y, c.w, c.h, "out.mp4")
		if len(cmd.Args) != 0 {
			t.Fatalf("crop %+v must yield a zero Cmd, got %#v", c, cmd.Args)
		}
	}
}

// Cropping from the top-left origin (x=0, y=0) is valid and must be
// accepted: ffmpeg handles it fine and it is the most common crop anchor.
func TestBuildCropCmd_OriginOffsetsAreAccepted(t *testing.T) {
	cmd := BuildCropCmd("in.mp4", 0, 0, 640, 360, "out.mp4")
	if len(cmd.Args) == 0 {
		t.Fatal("crop at origin must produce a command")
	}
	joined := strings.Join(cmd.Args, " ")
	if !strings.Contains(joined, "crop=w=640:h=360:x=0:y=0") {
		t.Fatalf("unexpected crop filter args: %s", joined)
	}
}

// The HDR branch composes setparams after the geometric filter for every
// transform mode and re-encodes in 10-bit HEVC.
func TestBuildTransformCmds_HDRComposeAfterGeometry(t *testing.T) {
	cases := []struct {
		name   string
		build  func() []string
		wantVF string
	}{
		{"rotate90", func() []string { return BuildRotateCmd("in.mkv", 90, "out.mkv", testHDRProps).Args }, "transpose=1," + testHDRSuffix},
		{"rotate180", func() []string { return BuildRotateCmd("in.mkv", 180, "out.mkv", testHDRProps).Args }, "hflip,vflip," + testHDRSuffix},
		{"fliph", func() []string { return BuildFlipCmd("in.mkv", "h", "out.mkv", testHDRProps).Args }, "hflip," + testHDRSuffix},
		{"crop", func() []string { return BuildCropCmd("in.mkv", 0, 0, 320, 240, "out.mkv", testHDRProps).Args }, "crop=w=320:h=240:x=0:y=0," + testHDRSuffix},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			args := c.build()
			if got := flagValue(args, "-vf"); got != c.wantVF {
				t.Fatalf("-vf = %q, want %q", got, c.wantVF)
			}
			if got := flagValue(args, "-c:v"); got != "libx265" {
				t.Fatalf("-c:v = %q, want libx265", got)
			}
			if got := flagValue(args, "-crf"); got != "22" {
				t.Fatalf("-crf = %q, want 22", got)
			}
			if got := flagValue(args, "-preset"); got != "medium" {
				t.Fatalf("-preset = %q, want medium", got)
			}
			if got := flagValue(args, "-x265-params"); got != testHDRParams {
				t.Fatalf("-x265-params = %q, want %q", got, testHDRParams)
			}
			if got := flagValue(args, "-pix_fmt"); got != "" {
				t.Fatalf("the HDR branch must not force -pix_fmt, got %q", got)
			}
			if got := flagValue(args, "-c:a"); got != "copy" {
				t.Fatalf("-c:a = %q, want copy", got)
			}
		})
	}
}

func TestRotateOutputName(t *testing.T) {
	cases := map[int]string{
		90:  "clip_rot90.mp4",
		180: "clip_rot180.mp4",
		270: "clip_rot270.mp4",
	}
	for degrees, want := range cases {
		if got := RotateOutputName("/a/b/clip.mp4", degrees); got != want {
			t.Fatalf("RotateOutputName(%d) = %q want %q", degrees, got, want)
		}
	}
}

func TestFlipOutputName(t *testing.T) {
	if got, want := FlipOutputName("/a/b/clip.mp4", "h"), "clip_fliph.mp4"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	if got, want := FlipOutputName("/a/b/clip.mp4", "v"), "clip_flipv.mp4"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestCropOutputName(t *testing.T) {
	if got, want := CropOutputName("/a/b/clip.mp4"), "clip_cropped.mp4"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func equalArgs(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
