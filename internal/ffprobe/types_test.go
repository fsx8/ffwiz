package ffprobe

import (
	"encoding/json"
	"testing"
)

func TestProbeResultJSONUnmarshal(t *testing.T) {
	const sample = `{
  "streams": [
    {"index": 0, "codec_name": "h264", "codec_type": "video", "width": 1920, "height": 1080, "r_frame_rate": "30000/1001", "sample_aspect_ratio": "1:1", "codec_tag_string": "avc1", "codec_tag": "0x31637661"},
    {"index": 2, "codec_name": "aac", "codec_type": "audio", "sample_rate": "48000", "channels": 2, "tags": {"language": "eng"}}
  ],
  "format": {"format_long_name": "Matroska / WebM", "duration": "12.34", "size": "123456", "bit_rate": "1000000"}
}`

	var res ProbeResult
	if err := json.Unmarshal([]byte(sample), &res); err != nil {
		t.Fatal(err)
	}
	if res.Format.FormatLongName != "Matroska / WebM" {
		t.Fatalf("unexpected format: %#v", res.Format)
	}
	if len(res.Streams) != 2 {
		t.Fatalf("unexpected streams: %#v", res.Streams)
	}
	if res.Streams[0].CodecType != "video" || res.Streams[0].Width != 1920 {
		t.Fatalf("unexpected video stream: %#v", res.Streams[0])
	}
	if res.Streams[0].CodecTagString != "avc1" {
		t.Fatalf("codec_tag_string = %q, want avc1", res.Streams[0].CodecTagString)
	}
	if res.Streams[1].CodecType != "audio" || res.Streams[1].SampleRate != "48000" {
		t.Fatalf("unexpected audio stream: %#v", res.Streams[1])
	}
}

func TestProbeResultHelpers(t *testing.T) {
	res := &ProbeResult{
		Streams: []Stream{
			{Index: 0, CodecType: "video", CodecName: "h264", Disposition: map[string]any{"attached_pic": float64(0)}},
			{Index: 1, CodecType: "audio", CodecName: "aac"},
			{Index: 2, CodecType: "video", CodecName: "mjpeg", Disposition: map[string]any{"attached_pic": float64(1)}},
			{Index: 3, CodecType: "subtitle", CodecName: "subrip"},
		},
		Format: Format{Duration: "12.5"},
	}

	if !res.HasAudio() || !res.HasVideo() {
		t.Fatal("expected audio and (playable) video")
	}
	if got := len(res.VideoStreams()); got != 1 {
		t.Fatalf("VideoStreams = %d, want 1 (cover art excluded)", got)
	}
	if s := res.FirstVideo(); s == nil || s.CodecName != "h264" {
		t.Fatalf("FirstVideo = %+v, want the h264 stream", s)
	}
	if got := len(res.StreamsOfType("subtitle")); got != 1 {
		t.Fatalf("StreamsOfType(subtitle) = %d, want 1", got)
	}
	if d, ok := res.Duration(); !ok || d != 12.5 {
		t.Fatalf("Duration = %v %v, want 12.5 true", d, ok)
	}

	coverOnly := &ProbeResult{Streams: []Stream{
		{CodecType: "video", Disposition: map[string]any{"attached_pic": float64(1)}},
	}}
	if coverOnly.HasVideo() {
		t.Fatal("cover-art-only input must not count as video")
	}
	if s := coverOnly.FirstVideo(); s != nil {
		t.Fatalf("FirstVideo must be nil for cover-art-only input, got %+v", s)
	}

	var nilRes *ProbeResult
	if _, ok := nilRes.Duration(); ok {
		t.Fatal("nil result must report no duration")
	}
	if nilRes.HasAudio() || nilRes.HasVideo() {
		t.Fatal("nil result must report no streams")
	}

	if !(Stream{Disposition: map[string]any{"attached_pic": true}}).IsAttachedPic() {
		t.Fatal("bool disposition values must be recognized")
	}
}

// ffprobe emits "N/A" for unknown durations; such values (and anything
// non-positive) must read as "no usable duration", not an error or 0s.
func TestProbeResultDurationHandlesNA(t *testing.T) {
	cases := []struct {
		raw    string
		want   float64
		wantOK bool
	}{
		{"12.5", 12.5, true},
		{"N/A", 0, false},
		{"", 0, false},
		{" 7 ", 7, true},
		{"0", 0, false},
		{"-3", 0, false},
		{"junk", 0, false},
	}
	for _, c := range cases {
		res := &ProbeResult{Format: Format{Duration: c.raw}}
		got, ok := res.Duration()
		if ok != c.wantOK || got != c.want {
			t.Fatalf("Duration(%q) = %v %v, want %v %v", c.raw, got, ok, c.want, c.wantOK)
		}
	}
}

// stream_side_data_list arrives in more than one shape depending on the
// ffprobe version and on whether the values came from the container or a
// decoded bitstream SEI: plain integers or "numerator/denominator"
// rationals. Both must parse, unknown side_data_type entries must be
// ignored, and an absent list must simply find nothing.
func TestStreamSideDataParse(t *testing.T) {
	// Shape 1: integer values as container-formats store them (already
	// scaled: coordinates 0.00002, luminance 0.0001 cd/m²).
	const ints = `{
	  "streams": [{
	    "codec_type": "video",
	    "side_data_list": [],
	    "stream_side_data_list": [
	      {"side_data_type": "Mastering display metadata",
	       "red_x": 35400, "red_y": 14600,
	       "green_x": 8500, "green_y": 39850,
	       "blue_x": 6550, "blue_y": 2300,
	       "white_point_x": 15635, "white_point_y": 16450,
	       "min_luminance": 1, "max_luminance": 10000000},
	      {"side_data_type": "Content light level",
	       "max_content": 1000, "max_average": 400}
	    ]
	  }]
	}`

	// Shape 2: rational strings as decoded SEI side data prints them,
	// plus an unknown entry type and the "... metadata" spelling of the
	// content light level.
	const rationals = `{
	  "streams": [{
	    "codec_type": "video",
	    "stream_side_data_list": [
	      {"side_data_type": "H.26[45] User Data Unregistered SEI message"},
	      {"side_data_type": "Mastering display metadata",
	       "red_x": "35400/50000", "red_y": "14600/50000",
	       "green_x": "8500/50000", "green_y": "39850/50000",
	       "blue_x": "6550/50000", "blue_y": "2300/50000",
	       "white_point_x": "15635/50000", "white_point_y": "16450/50000",
	       "min_luminance": "1/10000", "max_luminance": "10000000/10000"},
	      {"side_data_type": "Content light level metadata",
	       "max_content": 1000, "max_average": 400}
	    ]
	  }]
	}`

	for name, sample := range map[string]string{"integers": ints, "rationals": rationals} {
		t.Run(name, func(t *testing.T) {
			var res ProbeResult
			if err := json.Unmarshal([]byte(sample), &res); err != nil {
				t.Fatalf("tolerant parse failed: %v", err)
			}
			s := res.Streams[0]
			md, ok := s.MasteringDisplay()
			if !ok {
				t.Fatal("mastering display side data not found")
			}
			// Both shapes must land on x265's scaled-integer spelling.
			for _, c := range []struct {
				label     string
				got, want int64
			}{
				{"red_x", md.RedX.Scaled(50000), 35400},
				{"red_y", md.RedY.Scaled(50000), 14600},
				{"green_x", md.GreenX.Scaled(50000), 8500},
				{"green_y", md.GreenY.Scaled(50000), 39850},
				{"blue_x", md.BlueX.Scaled(50000), 6550},
				{"blue_y", md.BlueY.Scaled(50000), 2300},
				{"white_x", md.WhiteX.Scaled(50000), 15635},
				{"white_y", md.WhiteY.Scaled(50000), 16450},
				{"min_lum", md.MinLuminance.Scaled(10000), 1},
				{"max_lum", md.MaxLuminance.Scaled(10000), 10000000},
			} {
				if c.got != c.want {
					t.Errorf("%s = %d, want %d", c.label, c.got, c.want)
				}
			}
			cl, ok := s.ContentLight()
			if !ok {
				t.Fatal("content light side data not found")
			}
			if cl.MaxContent.Scaled(1) != 1000 || cl.MaxAverage.Scaled(1) != 400 {
				t.Fatalf("content light = %d/%d, want 1000/400", cl.MaxContent.Scaled(1), cl.MaxAverage.Scaled(1))
			}
		})
	}

	var noSideData ProbeResult
	if err := json.Unmarshal([]byte(`{"streams": [{"codec_type": "video"}]}`), &noSideData); err != nil {
		t.Fatal(err)
	}
	if _, ok := noSideData.Streams[0].MasteringDisplay(); ok {
		t.Error("absent side data must not report mastering display")
	}
	if _, ok := noSideData.Streams[0].ContentLight(); ok {
		t.Error("absent side data must not report content light")
	}

	// Junk in a side-data value degrades to zero instead of failing the
	// whole probe parse.
	var junk ProbeResult
	if err := json.Unmarshal([]byte(`{"streams": [{"codec_type": "video", "stream_side_data_list": [
		{"side_data_type": "Mastering display metadata", "red_x": "nonsense", "max_luminance": "1/0"}]}]}`), &junk); err != nil {
		t.Fatalf("junk values must not abort the parse: %v", err)
	}
	md, _ := junk.Streams[0].MasteringDisplay()
	if md.RedX.Num != 0 || md.MaxLuminance.Num != 0 {
		t.Errorf("junk values must degrade to zero, got %+v", md)
	}
}
