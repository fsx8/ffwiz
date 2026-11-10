package ffmpeg

type Cmd struct {
	Args []string
}

func (c Cmd) FullArgs() []string {
	out := make([]string, 0, 1+len(c.Args))
	out = append(out, "ffmpeg")
	out = append(out, c.Args...)
	return out
}

type TrackType string

const (
	TrackVideo    TrackType = "video"
	TrackAudio    TrackType = "audio"
	TrackSubtitle TrackType = "subtitle"
)

type Track struct {
	Index int
	Type  TrackType
	Codec string
	// Color carries the probed color props of the source stream (zero for
	// SDR or unknown); a libx265 conversion of an HDR/10-bit track gets the
	// 10-bit preserving branch instead of defaulting to 8-bit.
	Color ColorProps
}

type TrackAction string

const (
	ActionRemove  TrackAction = "remove"
	ActionKeep    TrackAction = "keep"
	ActionConvert TrackAction = "convert"
)

type TrackActionInfo struct {
	Action TrackAction
	Codec  string // for convert
}
