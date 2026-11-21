package app

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
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
	"github.com/fsx8/ffwiz/internal/media"
)

type tracksDoneMsg struct {
	tracks []trackView
	err    error
}

// addProbeMsg carries a probed sidecar file back into the add step.
type addProbeMsg struct {
	path string
	res  *ffprobe.ProbeResult
	err  error
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

	step string // "tracks" | "add" | "codec" | "output" | "confirm"

	codecList     list.Model
	codecBulk     bool          // codec choice applies to every track of codecBulkType
	codecBulkType ffx.TrackType // captured when the chooser opens
	out           textinput.Model
	guard         overwriteGuard
	vp            viewport.Model // scrolls the track list; keys stay with Update

	// Add-track browser (step "add"): browses for external subtitle/audio
	// files and probes the picked one asynchronously before appending its
	// streams as new tracks.
	addDir     string
	addList    list.Model
	addListW   int // kept across refreshes so navigation preserves the size
	addListH   int
	probing    bool   // an add probe is in flight; later results are stale
	addingPath string // file being probed, named on the spinner line

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
		addList:  newAddList(),
		style:    lipgloss.NewStyle().Padding(1, 2),
	}
}

// newAddList builds an initialized, empty add-browser list. bubbles'
// SetSize panics on a zero-value Model, and the first WindowSizeMsg
// arrives long before the browser is ever opened.
func newAddList() list.Model {
	l := list.New(nil, list.NewDefaultDelegate(), 0, 0)
	l.Title = "Add external subtitle/audio tracks"
	l.SetFilteringEnabled(true)
	l.DisableQuitKeybindings()
	return l
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
		m.addListW, m.addListH = dim(msg.Width, 4), dim(msg.Height, 6)
		m.addList.SetSize(m.addListW, m.addListH)
		m.resizeViewport()
	case tea.KeyMsg:
		typing := m.step == "output" && textInputFocused(m.out)
		switch msg.String() {
		case "q":
			if typing {
				break
			}
			if m.step == "add" && filterActive(m.addList) {
				break // q types into the add browser's filter
			}
			return m, tea.Quit
		case "esc":
			// Esc walks exactly one step back along the flow
			// (tracks -> output -> confirm; codec/add branch off tracks).
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
			case "add":
				if filterActive(m.addList) {
					break // let the list clear/leave its filter
				}
				// Leave the browser; an in-flight probe's result is
				// stale afterwards and gets ignored (m.probing off).
				m.probing = false
				m.err = ""
				m.step = "tracks"
				return m, nil
			default:
				return m, pop()
			}
		}
	}

	switch msg := msg.(type) {
	case spinner.TickMsg:
		if m.loading || m.probing {
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
	case addProbeMsg:
		if !m.probing || m.step != "add" {
			return m, nil // stale: the user already left the add step
		}
		m.probing = false
		if msg.err != nil {
			m.err = msg.err.Error()
			return m, nil
		}
		added := addedTracksFromProbe(msg.res, msg.path)
		if len(added) == 0 {
			m.err = "no subtitle or audio tracks found in " + filepath.Base(msg.path)
			return m, nil
		}
		m.err = ""
		first := len(m.tracks)
		m.tracks = append(m.tracks, added...)
		for i := first; i < len(m.tracks); i++ {
			m.actions[i] = ffx.TrackActionInfo{Action: ffx.ActionKeep}
		}
		m.cursor = first // land on the first newly added entry
		// Back to the tracks step so the NEW lines and the cursor
		// landing are visible; `a` reopens the browser at the same
		// directory for the next import.
		m.step = "tracks"
		return m, nil
	}

	if m.loading {
		return m, nil
	}

	switch m.step {
	case "tracks":
		return m.updateTracksStep(msg)
	case "add":
		return m.updateAddStep(msg)
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
			// An imported track has no place in the input file, so
			// removing it means deleting it from the list entirely —
			// the output is exactly as if it had never been added.
			if m.tracks[m.cursor].Track.SourceFile != "" {
				m.deleteAddedTrack(m.cursor)
				return m, nil
			}
			m.actions[m.cursor] = ffx.TrackActionInfo{Action: ffx.ActionRemove}
			return m, nil
		case "k":
			m.actions[m.cursor] = ffx.TrackActionInfo{Action: ffx.ActionKeep}
			return m, nil
		case "a":
			m.openAddBrowser()
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

// addTrackExtensions lists the file types offered as import sources in the
// add browser: external subtitle and audio files. It is deliberately its
// own set — growing media.go's global video list with subtitle extensions
// would make bare .srt files show up in every other file listing.
var addTrackExtensions = map[string]bool{
	".srt": true, ".ass": true, ".ssa": true, ".vtt": true, ".sup": true,
	".mka": true, ".mp3": true, ".flac": true, ".wav": true,
	".ogg": true, ".m4a": true, ".aac": true, ".opus": true,
}

// addFileItem is a selectable external subtitle/audio file in the add
// browser.
type addFileItem struct {
	name string
	path string
}

func (i addFileItem) Title() string       { return i.name }
func (i addFileItem) Description() string { return "" }
func (i addFileItem) FilterValue() string { return i.name }

// openAddBrowser branches to the add step, starting the browse next to the
// loaded media file and remembering the last directory across adds.
func (m *tracksWizard) openAddBrowser() {
	if m.addDir == "" {
		m.addDir = filepath.Dir(m.filePath)
	}
	m.err = ""
	m.refreshAddFiles()
	m.step = "add"
}

// isAddTrackFile reports whether a directory entry is an addable side
// file per addTrackExtensions.
func isAddTrackFile(name string) bool {
	return addTrackExtensions[strings.ToLower(filepath.Ext(name))]
}

// refreshAddFiles rebuilds the add browser for m.addDir: parent entry,
// subdirectories, then subtitle/audio files per addTrackExtensions. Video
// files are deliberately absent — adding video tracks is not supported.
func (m *tracksWizard) refreshAddFiles() {
	files, dirs, err := media.ListDirMatching(m.addDir, isAddTrackFile)
	var items []list.Item
	if err != nil {
		m.err = err.Error()
	} else {
		m.err = ""
		if parent := filepath.Dir(m.addDir); parent != m.addDir {
			items = append(items, dirItem{name: "..", path: parent})
		}
		for _, d := range dirs {
			items = append(items, dirItem{name: d, path: filepath.Join(m.addDir, d)})
		}
		for _, f := range files {
			if addTrackExtensions[strings.ToLower(filepath.Ext(f))] {
				items = append(items, addFileItem{name: f, path: filepath.Join(m.addDir, f)})
			}
		}
	}
	l := newAddList()
	l.SetItems(items)
	if m.addListW > 0 {
		l.SetSize(m.addListW, m.addListH)
	}
	m.addList = l
}

// updateAddStep runs the add browser. Enter on a directory navigates,
// Enter on a file starts the add probe; while probing the list is frozen.
func (m *tracksWizard) updateAddStep(msg tea.Msg) (tea.Model, tea.Cmd) {
	if km, ok := msg.(tea.KeyMsg); ok {
		switch km.String() {
		case "q":
			if filterActive(m.addList) {
				break
			}
			return m, tea.Quit
		case "enter":
			if filterActive(m.addList) || m.probing {
				break
			}
			switch it := m.addList.SelectedItem().(type) {
			case dirItem:
				m.addDir = it.path
				m.refreshAddFiles()
				return m, nil
			case addFileItem:
				return m, m.probeAddCmd(it.path)
			}
			return m, nil
		}
	}
	if m.probing {
		return m, nil
	}
	var cmd tea.Cmd
	m.addList, cmd = m.addList.Update(msg)
	return m, cmd
}

// probeAddCmd probes the picked sidecar file off the UI thread (bounded by
// probeTimeout). No cancel func is stored: the result handler ignores
// anything arriving after the user left the add step (m.probing cleared),
// and probeTimeout bounds a stuck probe on its own.
func (m *tracksWizard) probeAddCmd(path string) tea.Cmd {
	m.probing = true
	m.addingPath = path
	m.err = ""
	return func() tea.Msg {
		if m.cfg.Prober == nil {
			return addProbeMsg{path: path, err: errors.New("ffprobe unavailable")}
		}
		ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
		defer cancel()
		res, err := m.cfg.Prober.Probe(ctx, path)
		return addProbeMsg{path: path, res: res, err: err}
	}
}

// addedTracksFromProbe turns a sidecar file's probe into imported track
// entries: its subtitle and audio streams become NEW tracks (video and
// cover art are deliberately not importable), each carrying its source
// file and stream index so the generator maps it from its own input.
func addedTracksFromProbe(res *ffprobe.ProbeResult, path string) []trackView {
	if res == nil {
		return nil
	}
	var out []trackView
	for _, s := range res.Streams {
		switch s.CodecType {
		case "audio", "subtitle":
		default:
			continue
		}
		out = append(out, trackView{
			Track: ffx.Track{
				Index:       s.Index,
				Type:        ffx.TrackType(s.CodecType),
				Codec:       s.CodecName,
				SourceFile:  path,
				SourceIndex: s.Index,
			},
			SampleRate: s.SampleRate,
			Channels:   s.Channels,
			Language:   s.Tags["language"],
			Title:      s.Tags["title"],
		})
	}
	return out
}

// deleteAddedTrack removes an imported track from the list — the command
// equivalent of never adding it. The actions map is keyed by list
// position, so every entry after the deleted one shifts down one slot.
func (m *tracksWizard) deleteAddedTrack(idx int) {
	m.tracks = slices.Delete(m.tracks, idx, idx+1)
	actions := make(map[int]ffx.TrackActionInfo, len(m.actions))
	for i, info := range m.actions {
		switch {
		case i < idx:
			actions[i] = info
		case i > idx:
			actions[i-1] = info
		}
	}
	m.actions = actions
	m.cursor = min(max(m.cursor, 0), len(m.tracks)-1)
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
	case "add":
		if m.probing {
			return m.style.Render(m.spin.View() + " Probing " + filepath.Base(m.addingPath) + "…\n\nEsc to cancel")
		}
		info := lipgloss.NewStyle().Foreground(lipgloss.Color("241")).Render(
			"Directory: " + m.addDir + "\n" +
				"/ to filter • Enter adds the file's subtitle/audio tracks • Esc back to tracks • q to quit")
		errLine := renderErrLine(m.err)
		return m.style.Render(m.addList.View() + "\n" + info + errLine)
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
	help := lipgloss.NewStyle().Foreground(lipgloss.Color("241")).Render("↑/↓ navigate • r/k/c track • K/R all • C all of type • a add track • Enter continue • Esc back • q to quit")
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
		} else if action == ffx.ActionKeep && tv.Track.SourceFile != "" {
			// Keeping an imported track means adding it to the output;
			// label it NEW so the merge intent stays visible.
			actionStr = "NEW"
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

// truncateBudget shortens s to budget runes, ellipsized when longer.
func truncateBudget(s string, budget int) string {
	if r := []rune(s); len(r) > budget {
		return string(r[:budget-1]) + "…"
	}
	return s
}

// titleSuffix renders a track title as a trailing list segment, truncated
// with an ellipsis when it exceeds the budget.
func titleSuffix(title string) string {
	if title == "" {
		return ""
	}
	return "  " + truncateBudget(title, trackTitleBudget)
}

func trackLine(i int, tv trackView) string {
	if tv.Track.SourceFile != "" {
		return externalTrackLine(i, tv)
	}
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

// externalTrackLine renders an imported track: the source file's base name
// (budget-truncated like every other variable segment) replaces the source
// stream index, which says nothing about where the track came from.
func externalTrackLine(i int, tv trackView) string {
	lang := tv.Language
	if lang == "" {
		lang = "und"
	}
	file := "file=" + truncateBudget(filepath.Base(tv.Track.SourceFile), trackTitleBudget)
	switch tv.Track.Type {
	case ffx.TrackAudio:
		return "[" + strconv.Itoa(i) + "] AUDIO  " + file + "  " + tv.Track.Codec + "  " + tv.SampleRate + "Hz  " + strconv.Itoa(tv.Channels) + "ch  " + lang + titleSuffix(tv.Title)
	case ffx.TrackSubtitle:
		return "[" + strconv.Itoa(i) + "] SUBS   " + file + "  " + tv.Track.Codec + "  " + lang + titleSuffix(tv.Title)
	case ffx.TrackVideo:
		return "[" + strconv.Itoa(i) + "] VIDEO  " + file + "  " + tv.Track.Codec + titleSuffix(tv.Title)
	default:
		return "[" + strconv.Itoa(i) + "] " + string(tv.Track.Type) + " " + file + titleSuffix(tv.Title)
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
