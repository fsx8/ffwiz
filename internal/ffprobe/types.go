package ffprobe

import (
	"math"
	"strconv"
	"strings"
)

type ProbeResult struct {
	Streams []Stream `json:"streams"`
	Format  Format   `json:"format"`
}

type Format struct {
	Filename       string            `json:"filename"`
	NBStreams      int               `json:"nb_streams"`
	NBPrograms     int               `json:"nb_programs"`
	FormatName     string            `json:"format_name"`
	FormatLongName string            `json:"format_long_name"`
	StartTime      string            `json:"start_time"`
	Duration       string            `json:"duration"`
	Size           string            `json:"size"`
	BitRate        string            `json:"bit_rate"`
	ProbeScore     int               `json:"probe_score"`
	Tags           map[string]string `json:"tags"`
}

type Stream struct {
	Index             int               `json:"index"`
	CodecName         string            `json:"codec_name"`
	CodecLongName     string            `json:"codec_long_name"`
	CodecType         string            `json:"codec_type"` // "video" | "audio" | "subtitle"
	CodecTagString    string            `json:"codec_tag_string"`
	Width             int               `json:"width"`
	Height            int               `json:"height"`
	RFrameRate        string            `json:"r_frame_rate"`
	SampleRate        string            `json:"sample_rate"`
	Channels          int               `json:"channels"`
	BitRate           string            `json:"bit_rate"`
	Duration          string            `json:"duration"`
	SampleAspectRatio string            `json:"sample_aspect_ratio"`
	Profile           string            `json:"profile"`
	Level             int               `json:"level"`
	PixFmt            string            `json:"pix_fmt"`
	ColorTransfer     string            `json:"color_transfer"`
	ColorPrimaries    string            `json:"color_primaries"`
	ColorSpace        string            `json:"color_space"`
	SideData          []StreamSideData  `json:"stream_side_data_list"`
	Disposition       map[string]any    `json:"disposition"`
	Tags              map[string]string `json:"tags"`
}

// SideRational is a defensive number for side-data values: ffprobe emits
// them either as plain JSON numbers or as "numerator/denominator" strings
// depending on the ffprobe version and on whether the value came from the
// container or a decoded bitstream SEI. Anything unparseable degrades to
// zero instead of failing the whole probe parse.
type SideRational struct{ Num, Den int64 }

func (r *SideRational) UnmarshalJSON(b []byte) error {
	*r = SideRational{}
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		return nil
	}
	num, den, hasDen := strings.Cut(s, "/")
	n, err := strconv.ParseInt(strings.TrimSpace(num), 10, 64)
	if err != nil {
		return nil // tolerate junk rather than aborting the probe
	}
	if hasDen {
		d, err := strconv.ParseInt(strings.TrimSpace(den), 10, 64)
		if err != nil || d == 0 {
			return nil // a broken rational is not a number: degrade to zero
		}
		r.Num, r.Den = n, d
		return nil
	}
	r.Num = n
	return nil
}

// Scaled returns the value in the scaled integer spelling x265's
// master-display parameter expects (coordinates pre-scaled by 0.00002,
// luminance by 0.0001 cd/m²): a rational "35400/50000" becomes 35400,
// while a bare number is already stored in exactly those units.
func (r SideRational) Scaled(scale int64) int64 {
	if r.Den == 0 {
		return r.Num
	}
	return int64(math.Round(float64(r.Num) * float64(scale) / float64(r.Den)))
}

// StreamSideData is one entry of a stream's stream_side_data_list. Only
// the mastering-display and content-light entries are interpreted (HDR10
// preservation); unknown side_data_type entries are carried as-is and
// ignored by callers.
type StreamSideData struct {
	SideDataType string `json:"side_data_type"`
	// Mastering display metadata: primary/white-point chromaticities as
	// 0.00002-increment values, luminance as 0.0001 cd/m² values.
	RedX         SideRational `json:"red_x"`
	RedY         SideRational `json:"red_y"`
	GreenX       SideRational `json:"green_x"`
	GreenY       SideRational `json:"green_y"`
	BlueX        SideRational `json:"blue_x"`
	BlueY        SideRational `json:"blue_y"`
	WhiteX       SideRational `json:"white_point_x"`
	WhiteY       SideRational `json:"white_point_y"`
	MinLuminance SideRational `json:"min_luminance"`
	MaxLuminance SideRational `json:"max_luminance"`
	// Content light level metadata: cd/m² integers.
	MaxContent SideRational `json:"max_content"`
	MaxAverage SideRational `json:"max_average"`
}

// MasteringDisplay returns the stream's "Mastering display metadata" side
// data; ok is false when the probe carried none (older ffprobe versions do
// not emit stream_side_data_list at all).
func (s Stream) MasteringDisplay() (StreamSideData, bool) {
	for _, sd := range s.SideData {
		if sd.SideDataType == "Mastering display metadata" {
			return sd, true
		}
	}
	return StreamSideData{}, false
}

// ContentLight returns the stream's content light level side data; ok is
// false when absent. ffprobe spells the type name slightly differently
// across versions ("Content light level" vs "... level metadata").
func (s Stream) ContentLight() (StreamSideData, bool) {
	for _, sd := range s.SideData {
		if sd.SideDataType == "Content light level" || sd.SideDataType == "Content light level metadata" {
			return sd, true
		}
	}
	return StreamSideData{}, false
}

// Duration returns the container duration in seconds; ok is false when the
// probe carries no usable duration.
func (r *ProbeResult) Duration() (float64, bool) {
	if r == nil {
		return 0, false
	}
	d, err := strconv.ParseFloat(strings.TrimSpace(r.Format.Duration), 64)
	if err != nil || d <= 0 {
		return 0, false
	}
	return d, true
}

// StreamsOfType returns every stream with the given codec_type.
func (r *ProbeResult) StreamsOfType(codecType string) []Stream {
	if r == nil {
		return nil
	}
	var out []Stream
	for _, s := range r.Streams {
		if s.CodecType == codecType {
			out = append(out, s)
		}
	}
	return out
}

// HasAudio reports whether the file carries at least one audio stream.
func (r *ProbeResult) HasAudio() bool {
	return len(r.StreamsOfType("audio")) > 0
}

// VideoStreams returns the video streams, excluding attached pictures
// (embedded cover art), which are not playable video.
func (r *ProbeResult) VideoStreams() []Stream {
	if r == nil {
		return nil
	}
	var out []Stream
	for _, s := range r.Streams {
		if s.CodecType == "video" && !s.IsAttachedPic() {
			out = append(out, s)
		}
	}
	return out
}

// HasVideo reports whether the file carries playable video (cover art does
// not count).
func (r *ProbeResult) HasVideo() bool {
	return len(r.VideoStreams()) > 0
}

// FirstVideo returns the first playable video stream, or nil.
func (r *ProbeResult) FirstVideo() *Stream {
	for i := range r.Streams {
		s := &r.Streams[i]
		if s.CodecType == "video" && !s.IsAttachedPic() {
			return s
		}
	}
	return nil
}

// IsAttachedPic reports whether the stream is an embedded cover art picture
// (disposition.attached_pic); such streams show up as "video" in ffprobe
// output but must not be treated as playable video.
func (s Stream) IsAttachedPic() bool {
	switch v := s.Disposition["attached_pic"].(type) {
	case float64:
		return v != 0
	case int:
		return v != 0
	case bool:
		return v
	}
	return false
}
