package app

import (
	"context"
	"errors"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/fsx8/ffwiz/internal/execx"
	ffx "github.com/fsx8/ffwiz/internal/ffmpeg"
	"github.com/fsx8/ffwiz/internal/ffprobe"
)

type tracksDoneMsg struct {
	tracks []trackView
	err    error
}

type trackView struct {
	Track      ffx.Track
	Width      int
	Height     int
	FPS        string
	SampleRate string
	Channels   int
	Language   string
	Title      string
}

type tracksWizard struct {
	cfg      Config
	filePath string

	loading bool
	spin    spinner.Model
	err     string

	tracks  []trackView
	actions map[int]ffx.TrackActionInfo
	cursor  int

	step string // "tracks" | "codec" | "output" | "confirm"

	codecList     list.Model
	codecBulk     bool          // codec choice applies to every track of codecBulkType
	codecBulkType ffx.TrackType // captured when the chooser opens
	out           textinput.Model
	guard         overwriteGuard
	vp            viewport.Model // scrolls the track list; keys stay with Update

	style lipgloss.Style
	w, h  int
}

func newTracksWizard(cfg Config, filePath string) *tracksWizard {
	sp := spinner.New()
	sp.Spinner = spinner.Dot

	out := newPathInput()

	return &tracksWizard{
		cfg:      cfg,
		filePath: filePath,
		loading:  true,
		spin:     sp,
		actions:  map[int]ffx.TrackActionInfo{},
		step:     "tracks",
		out:      out,
		style:    lipgloss.NewStyle().Padding(1, 2),
	}
}

func (m *tracksWizard) Init() tea.Cmd {
	return tea.Batch(m.spin.Tick, func() tea.Msg {
		if m.cfg.Prober == nil {
			return tracksDoneMsg{err: errors.New("ffprobe unavailable")}
		}
		ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
		defer cancel()
		res, err := m.cfg.Prober.Probe(ctx, m.filePath)
		if err != nil {
			return tracksDoneMsg{err: err}
		}
		tvs := tracksFromProbe(res)
		if len(tvs) == 0 {
			return tracksDoneMsg{err: errNoTracks{}}
		}
		return tracksDoneMsg{tracks: tvs}
	})
}

type errNoTracks struct{}

func (errNoTracks) Error() string { return "no tracks found in media file" }

func (m *tracksWizard) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		if m.step == "codec" {
			m.codecList.SetSize(dim(msg.Width, 4), dim(msg.Height, 6))
		}
		m.resizeViewport()
	case tea.KeyMsg:
		typing := m.step == "output" && textInputFocused(m.out)
		switch msg.String() {
		case "q":
			if typing {
				break
			}
			return m, tea.Quit
		case "esc":
			// Esc walks exactly one step back along the flow
			// (tracks -> output -> confirm; codec branches off tracks).
			switch m.step {
			case "confirm":
				m.guard.armedFor = ""
				m.err = ""
				m.step = "output"
				m.out.Focus()
				return m, textinput.Blink
			case "output":
				m.guard.armedFor = ""
				m.err = ""
				m.out.Blur()
				m.step = "tracks"
				return m, nil
			case "codec":
				m.err = ""
				m.codecBulk = false
				m.step = "tracks"
				return m, nil
			default:
				return m, pop()
			}
		}
	}

	switch msg := msg.(type) {
	case spinner.TickMsg:
		if m.loading {
			var cmd tea.Cmd
			m.spin, cmd = m.spin.Update(msg)
			return m, cmd
		}
	case tracksDoneMsg:
		m.loading = false
		if msg.err != nil {
			m.err = msg.err.Error()
			return m, nil
		}
		m.tracks = msg.tracks
		for i := range m.tracks {
			m.actions[i] = ffx.TrackActionInfo{Action: ffx.ActionKeep}
		}
		return m, nil
	}

	if m.loading {
		return m, nil
	}

	switch m.step {
	case "tracks":
		return m.updateTracksStep(msg)
	case "codec":
		var cmd tea.Cmd
		m.codecList, cmd = m.codecList.Update(msg)
		if km, ok := msg.(tea.KeyMsg); ok && km.String() == "enter" {
			if ch, ok := m.codecList.SelectedItem().(simpleItem); ok {
				if m.codecBulk {
					for i, tv := range m.tracks {
						if tv.Track.Type == m.codecBulkType {
							m.actions[i] = ffx.TrackActionInfo{Action: ffx.ActionConvert, Codec: ch.value}
						}
					}
				} else {
					m.actions[m.cursor] = ffx.TrackActionInfo{Action: ffx.ActionConvert, Codec: ch.value}
				}
				m.codecBulk = false
				m.step = "tracks"
				return m, nil
			}
		}
		return m, cmd
	case "output":
		if km, ok := msg.(tea.KeyMsg); ok && km.String() == "enter" {
			outName := strings.TrimSpace(m.out.Value())
			if outName == "" {
				m.err = "output file name is required"
				return m, nil
			}
			m.err = ""
			m.step = "confirm"
			return m, nil
		}
		var cmd tea.Cmd
		m.out, cmd = m.out.Update(msg)
		return m, cmd
	case "confirm":
		if km, ok := msg.(tea.KeyMsg); ok {
			switch km.String() {
			case "y", "enter":
				outPath := m.outputPath()
				// -y is passed to ffmpeg; make overwriting explicit.
				if m.guard.shouldWarn(outPath) {
					return m, nil
				}
				tracks := make([]ffx.Track, 0, len(m.tracks))
				for _, tv := range m.tracks {
					tracks = append(tracks, tv.Track)
				}
				cmd := ffx.BuildInteractiveConvertCmd(m.filePath, outPath, tracks, m.actions)
				if cmd == nil {
					m.err = "all tracks are removed; nothing to output"
					m.step = "tracks"
					return m, nil
				}
				return m, push(newExecScreen(m.cfg, "Converting…", execx.Cmd{Name: "ffmpeg", Args: cmd.Args}))
			case "n":
				m.guard.armedFor = ""
				m.step = "tracks"
				return m, nil
			}
		}
		return m, nil
	}

	return m, nil
}

func (m *tracksWizard) updateTracksStep(msg tea.Msg) (tea.Model, tea.Cmd) {
	// A failed probe leaves the track list empty; every key must be a no-op
	// (previously "c" indexed the empty slice and crashed the app).
	if len(m.tracks) == 0 {
		return m, nil
	}
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "up":
			if m.cursor > 0 {
				m.cursor--
			}
			return m, nil
		case "down":
			if m.cursor < len(m.tracks)-1 {
				m.cursor++
			}
			return m, nil
		case "r":
			m.actions[m.cursor] = ffx.TrackActionInfo{Action: ffx.ActionRemove}
			return m, nil
		case "k":
			m.actions[m.cursor] = ffx.TrackActionInfo{Action: ffx.ActionKeep}
			return m, nil
		case "K":
			for i := range m.tracks {
				m.actions[i] = ffx.TrackActionInfo{Action: ffx.ActionKeep}
			}
			return m, nil
		case "R":
			for i := range m.tracks {
				m.actions[i] = ffx.TrackActionInfo{Action: ffx.ActionRemove}
			}
			return m, nil
		case "c":
			m.openCodecChooser(m.tracks[m.cursor].Track.Type, false)
			return m, nil
		case "C":
			m.openCodecChooser(m.tracks[m.cursor].Track.Type, true)
			return m, nil
		case "enter":
			m.out.SetValue(defaultModifiedName(m.filePath))
			m.out.CursorEnd()
			m.out.Focus()
			m.err = ""
			m.step = "output"
			return m, textinput.Blink
		}
	}
	return m, nil
}

// openCodecChooser branches to the codec step. In bulk mode one choice is
// applied to every track of the given type; the type is captured here so a
// later cursor move cannot skew the bulk apply.
func (m *tracksWizard) openCodecChooser(tt ffx.TrackType, bulk bool) {
	opts := ffx.CodecOptions(tt)
	items := make([]list.Item, 0, len(opts))
	for _, o := range opts {
		items = append(items, simpleItem{value: o})
	}
	l := list.New(items, list.NewDefaultDelegate(), 0, 0)
	if bulk {
		l.Title = "Select codec for all " + string(tt) + " tracks"
	} else {
		l.Title = "Select codec (" + string(tt) + ")"
	}
	l.SetFilteringEnabled(false)
	l.DisableQuitKeybindings()
	if m.w > 0 && m.h > 0 {
		l.SetSize(dim(m.w, 4), dim(m.h, 6))
	}
	m.codecList = l
	m.codecBulk = bulk
	m.codecBulkType = tt
	m.step = "codec"
}

// resizeViewport adapts the track-list viewport to the terminal. The
// chrome around the list is the title block, the blank separator, and the
// help/error lines, plus the view padding.
func (m *tracksWizard) resizeViewport() {
	m.vp.Width = dim(m.w, 4)
	m.vp.Height = dim(m.h, 8)
}

// followCursor scrolls the viewport so the cursor's track line stays
// visible. Must run after SetContent: SetYOffset clamps against the
// content length, and KeyMsgs are never forwarded to the viewport, so
// this manual adjustment is the only scrolling trigger.
func (m *tracksWizard) followCursor() {
	if m.vp.Height <= 0 {
		return
	}
	if m.cursor < m.vp.YOffset {
		m.vp.SetYOffset(m.cursor)
	} else if m.cursor >= m.vp.YOffset+m.vp.Height {
		m.vp.SetYOffset(m.cursor - m.vp.Height + 1)
	}
}

func (m *tracksWizard) View() string {
	if m.loading {
		return m.style.Render(m.spin.View() + " Analyzing tracks…\n\nEsc to go back")
	}

	switch m.step {
	case "codec":
		return m.style.Render(m.codecList.View() + "\n" + lipgloss.NewStyle().Foreground(lipgloss.Color("241")).Render("Enter to select • Esc to go back • q to quit"))
	case "output":
		errLine := renderErrLine(m.err)
		return m.style.Render("Output file:\n\n" + m.out.View() + errLine + "\n\nEnter to continue • Esc to go back • q to quit")
	case "confirm":
		cmdStr := m.previewCommand()
		errLine := renderErrLine(m.err)
		warnLine := renderWarnLine(m.guard)
		return m.style.Render("Generated command:\n\n" + lipgloss.NewStyle().Foreground(lipgloss.Color("241")).Render(cmdStr) + errLine + warnLine + "\n\nEnter/Y to run • N to cancel • Esc to go back • q to quit")
	default:
		return m.style.Render(m.tracksView())
	}
}

func (m *tracksWizard) tracksView() string {
	title := lipgloss.NewStyle().Bold(true).Render("Interactive Modify Tracks") + "\n\n"
	help := lipgloss.NewStyle().Foreground(lipgloss.Color("241")).Render("↑/↓ navigate • r/k/c track • K/R all • C all of type • Enter continue • Esc back • q to quit")
	errLine := renderErrLine(m.err)
	warnLine := m.hdrConvertLine()

	var sb strings.Builder
	for i, tv := range m.tracks {
		prefix := "  "
		if i == m.cursor {
			prefix = "> "
		}
		action := m.actions[i].Action
		actionStr := strings.ToUpper(string(action))
		if action == ffx.ActionConvert {
			actionStr += ":" + ffx.CleanCodecChoice(m.actions[i].Codec)
		}
		line := prefix + trackLine(i, tv) + "  [" + actionStr + "]"
		if i == m.cursor {
			line = lipgloss.NewStyle().Bold(true).Render(line)
		}
		sb.WriteString(line + "\n")
	}
	// SetContent preserves the offset; followCursor then keeps the
	// cursor's line in view (and scrolls only when the cursor moved or
	// the viewport was resized).
	m.vp.SetContent(sb.String())
	m.followCursor()
	return title + m.vp.View() + "\n" + help + warnLine + errLine
}

// hdrConvertLine renders the note for an armed video conversion: crushing
// codecs warn about the washed-out 8-bit output, libx265 gets the
// 10-bit-preserving note; "" when no conversion applies to an HDR source.
func (m *tracksWizard) hdrConvertLine() string {
	note := hdrConvertNote(m.tracks, m.actions)
	if note == "" {
		return ""
	}
	return "\n" + lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Render(note)
}

// hdrConvertNote inspects every armed video conversion and returns the one
// note that matters (a crushing warning wins over the preserving note —
// the destructive outcome deserves the louder message).
func hdrConvertNote(tracks []trackView, actions map[int]ffx.TrackActionInfo) string {
	info := ""
	for i, tv := range tracks {
		if tv.Track.Type != ffx.TrackVideo {
			continue
		}
		if !tv.Track.Color.HDR() {
			continue
		}
		ai, ok := actions[i]
		if !ok || ai.Action != ffx.ActionConvert {
			continue
		}
		switch ffx.CleanCodecChoice(ai.Codec) {
		case "libx265":
			if info == "" {
				info = preserveNoteFor(tv.Track.Color.Transfer, tv.Track.Color.PixFmt)
			}
		default:
			// Every other video codec choice (x264/VP9/VP8/AV1) encodes
			// 8-bit without tone mapping.
			return crushNoteFor(tv.Track.Color.Transfer, tv.Track.Color.PixFmt)
		}
	}
	return info
}

func tracksFromProbe(res *ffprobe.ProbeResult) []trackView {
	var out []trackView
	if res == nil {
		return nil
	}
	for _, s := range res.Streams {
		switch s.CodecType {
		case "video", "audio", "subtitle":
		default:
			continue
		}
		// Embedded cover art shows up as an mjpeg "video" stream; offering
		// keep/remove/convert on it is confusing, so skip it.
		if s.CodecType == "video" && s.IsAttachedPic() {
			continue
		}
		tv := trackView{
			Track: ffx.Track{
				Index: s.Index,
				Type:  ffx.TrackType(s.CodecType),
				Codec: s.CodecName,
				Color: colorProps(s),
			},
			Width:      s.Width,
			Height:     s.Height,
			FPS:        s.RFrameRate,
			SampleRate: s.SampleRate,
			Channels:   s.Channels,
			Language:   s.Tags["language"],
			Title:      s.Tags["title"],
		}
		out = append(out, tv)
	}
	return out
}

// trackTitleBudget caps rendered track titles so a long title can never
// wrap its line: the viewport scrolls exactly one line per track.
const trackTitleBudget = 28

// titleSuffix renders a track title as a trailing list segment, truncated
// with an ellipsis when it exceeds the budget.
func titleSuffix(title string) string {
	if title == "" {
		return ""
	}
	if r := []rune(title); len(r) > trackTitleBudget {
		title = string(r[:trackTitleBudget-1]) + "…"
	}
	return "  " + title
}

func trackLine(i int, tv trackView) string {
	switch tv.Track.Type {
	case ffx.TrackVideo:
		return "[" + strconv.Itoa(i) + "] VIDEO  idx=" + strconv.Itoa(tv.Track.Index) + "  " + tv.Track.Codec + "  " + strconv.Itoa(tv.Width) + "x" + strconv.Itoa(tv.Height) + "  " + tv.FPS + titleSuffix(tv.Title)
	case ffx.TrackAudio:
		lang := tv.Language
		if lang == "" {
			lang = "und"
		}
		return "[" + strconv.Itoa(i) + "] AUDIO  idx=" + strconv.Itoa(tv.Track.Index) + "  " + tv.Track.Codec + "  " + tv.SampleRate + "Hz  " + strconv.Itoa(tv.Channels) + "ch  " + lang + titleSuffix(tv.Title)
	case ffx.TrackSubtitle:
		lang := tv.Language
		if lang == "" {
			lang = "und"
		}
		return "[" + strconv.Itoa(i) + "] SUBS   idx=" + strconv.Itoa(tv.Track.Index) + "  " + tv.Track.Codec + "  " + lang + titleSuffix(tv.Title)
	default:
		return "[" + strconv.Itoa(i) + "] " + string(tv.Track.Type) + " idx=" + strconv.Itoa(tv.Track.Index)
	}
}

func defaultModifiedName(filePath string) string {
	ext := filepath.Ext(filePath)
	base := strings.TrimSuffix(filepath.Base(filePath), ext)
	return base + "_modified" + ext
}

func (m *tracksWizard) outputPath() string {
	outName := strings.TrimSpace(m.out.Value())
	if outName == "" {
		outName = defaultModifiedName(m.filePath)
	}
	return resolveOutputPath(filepath.Dir(m.filePath), outName)
}

func (m *tracksWizard) previewCommand() string {
	tracks := make([]ffx.Track, 0, len(m.tracks))
	for _, tv := range m.tracks {
		tracks = append(tracks, tv.Track)
	}
	cmd := ffx.BuildInteractiveConvertCmd(m.filePath, m.outputPath(), tracks, m.actions)
	if cmd == nil {
		return "ffmpeg (no output; all tracks removed)"
	}
	return strings.Join(cmd.FullArgs(), " ")
}
