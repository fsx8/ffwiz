package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/fsx8/ffwiz/internal/execx"
	ffx "github.com/fsx8/ffwiz/internal/ffmpeg"
	ffprobe "github.com/fsx8/ffwiz/internal/ffprobe"
)

type transformProbeMsg struct {
	width  int
	height int
	stream *ffprobe.Stream // nil when no playable video stream was found
	err    error
}

type transformWizard struct {
	cfg      Config
	filePath string

	modeList list.Model
	width    textinput.Model
	height   textinput.Model
	x        textinput.Model
	y        textinput.Model
	out      textinput.Model
	guard    overwriteGuard

	focus int // focused crop field: 0 = width, 1 = height, 2 = x, 3 = y

	mode    string // "rotate90" | "rotate180" | "rotate270" | "fliph" | "flipv" | "crop"
	step    string // "mode" | "probing" | "crop" | "output"
	cropW   int
	cropH   int
	cropX   int
	cropY   int
	srcW    int // probed source dimensions; 0 when unknown
	srcH    int
	hdr     hdrWarner // color-format note + props for the re-encoding modes
	probing bool
	spin    spinner.Model

	probeCancel context.CancelFunc // cancels the in-flight probe on ctrl+c

	err   string
	style lipgloss.Style
}

var transformModes = []list.Item{
	simpleItem{value: "Rotate 90°"},
	simpleItem{value: "Rotate 180°"},
	simpleItem{value: "Rotate 270°"},
	simpleItem{value: "Flip Horizontal"},
	simpleItem{value: "Flip Vertical"},
	simpleItem{value: "Crop…"},
}

var transformModeKeys = map[string]string{
	"Rotate 90°":      "rotate90",
	"Rotate 180°":     "rotate180",
	"Rotate 270°":     "rotate270",
	"Flip Horizontal": "fliph",
	"Flip Vertical":   "flipv",
	"Crop…":           "crop",
}

func newTransformWizard(cfg Config, filePath string) *transformWizard {
	l := list.New(transformModes, list.NewDefaultDelegate(), 0, 0)
	l.Title = "Rotate, flip, or crop the video"
	l.SetFilteringEnabled(false)
	l.DisableQuitKeybindings()

	sp := spinner.New()
	sp.Spinner = spinner.Dot
	width := newShortInput("required")
	height := newShortInput("required")
	x := newShortInput("required")
	y := newShortInput("required")
	out := newPathInput()

	return &transformWizard{
		cfg:      cfg,
		filePath: filePath,
		modeList: l,
		width:    width,
		height:   height,
		x:        x,
		y:        y,
		out:      out,
		step:     "mode",
		spin:     sp,
		style:    lipgloss.NewStyle().Padding(1, 2),
	}
}

func (m *transformWizard) Init() tea.Cmd { return nil }

func (m *transformWizard) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.modeList.SetSize(dim(msg.Width, 4), dim(msg.Height, 6))
	case transformProbeMsg:
		return m.applyProbe(msg)
	case spinner.TickMsg:
		if m.step == "probing" {
			var cmd tea.Cmd
			m.spin, cmd = m.spin.Update(msg)
			return m, cmd
		}
	case tea.KeyMsg:
		typing := (m.step == "output" && textInputFocused(m.out)) ||
			(m.step == "crop" && textInputFocused(m.width, m.height, m.x, m.y))
		switch msg.String() {
		case "q":
			if typing {
				break
			}
			return m, tea.Quit
		case "ctrl+c":
			if m.probeCancel != nil {
				m.probeCancel()
				m.probeCancel = nil
			}
			return m, tea.Quit
		case "esc":
			switch m.step {
			case "output":
				m.guard.armedFor = ""
				m.err = ""
				m.out.Blur()
				if m.mode == "crop" {
					m.step = "crop"
					m.refocusCrop()
					return m, textinput.Blink
				}
				m.step = "mode"
				return m, nil
			case "probing":
				// Abort the in-flight probe instead of letting it run to
				// completion behind a backed-out step.
				if m.probeCancel != nil {
					m.probeCancel()
					m.probeCancel = nil
				}
				m.probing = false
				m.err = ""
				m.step = "mode"
				return m, nil
			case "crop":
				m.err = ""
				m.blurCrop()
				m.step = "mode"
				return m, nil
			default:
				m.guard.armedFor = ""
				m.err = ""
				return m, pop()
			}
		case "enter":
			if m.step == "probing" {
				return m, nil
			}
			switch m.step {
			case "mode":
				return m.selectMode()
			case "crop":
				return m.applyCrop()
			case "output":
				return m.run()
			}
		case "tab", "down":
			if m.step == "crop" {
				m.focus = focusStep(m.focus, 4, 1)
				m.refocusCrop()
				return m, textinput.Blink
			}
		case "shift+tab", "up":
			if m.step == "crop" {
				m.focus = focusStep(m.focus, 4, -1)
				m.refocusCrop()
				return m, textinput.Blink
			}
		}
		if m.step == "probing" {
			return m, nil
		}
	}

	var cmd tea.Cmd
	switch m.step {
	case "crop":
		switch m.focus {
		case 0:
			m.width, cmd = m.width.Update(msg)
		case 1:
			m.height, cmd = m.height.Update(msg)
		case 2:
			m.x, cmd = m.x.Update(msg)
		case 3:
			m.y, cmd = m.y.Update(msg)
		}
	case "output":
		m.out, cmd = m.out.Update(msg)
	default:
		m.modeList, cmd = m.modeList.Update(msg)
	}
	return m, cmd
}

func (m *transformWizard) selectMode() (tea.Model, tea.Cmd) {
	fi, ok := m.modeList.SelectedItem().(simpleItem)
	if !ok {
		return m, nil
	}
	mode, ok := transformModeKeys[fi.value]
	if !ok {
		return m, nil
	}
	m.mode = mode
	// Every mode probes: crop needs the source dimensions for prefill and
	// validation, the re-encoding modes need the color-format warning.
	m.step = "probing"
	m.probing = true
	return m, tea.Batch(m.spin.Tick, m.probeCmd())
}

func (m *transformWizard) probeCmd() tea.Cmd {
	prober := m.cfg.Prober
	path := m.filePath
	if prober == nil {
		return func() tea.Msg { return transformProbeMsg{err: errors.New("no prober available")} }
	}
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	m.probeCancel = cancel
	return func() tea.Msg {
		defer cancel()
		res, err := prober.Probe(ctx, path)
		if err != nil {
			return transformProbeMsg{err: err}
		}
		if s := res.FirstVideo(); s != nil {
			return transformProbeMsg{width: s.Width, height: s.Height, stream: s}
		}
		return transformProbeMsg{err: errors.New("no video stream")}
	}
}

// applyProbe prefills the crop fields with a centered half-size window and
// remembers the source dimensions for validation. For the re-encoding
// modes it collects the HDR/10-bit note and the color props for the
// preserving branch instead. On probe failure the crop fields stay empty
// ("required" placeholders) and the user enters the values manually.
func (m *transformWizard) applyProbe(msg transformProbeMsg) (tea.Model, tea.Cmd) {
	if m.step != "probing" || !m.probing {
		return m, nil
	}
	m.probing = false

	if m.mode == "crop" {
		if msg.err == nil && msg.width > 0 && msg.height > 0 {
			m.srcW, m.srcH = msg.width, msg.height
			halfW := msg.width / 2
			halfH := msg.height / 2
			m.width.SetValue(strconv.Itoa(halfW))
			m.height.SetValue(strconv.Itoa(halfH))
			m.x.SetValue(strconv.Itoa((msg.width - halfW) / 2))
			m.y.SetValue(strconv.Itoa((msg.height - halfH) / 2))
		}
		m.focus = 0
		m.step = "crop"
		m.refocusCrop()
		return m, textinput.Blink
	}

	if msg.stream != nil {
		m.hdr.note = hdrPreserveNoteFor(*msg.stream)
		m.hdr.props = colorProps(*msg.stream)
	}
	return m.beginOutput()
}

func (m *transformWizard) applyCrop() (tea.Model, tea.Cmd) {
	w, errW := strconv.Atoi(strings.TrimSpace(m.width.Value()))
	h, errH := strconv.Atoi(strings.TrimSpace(m.height.Value()))
	x, errX := strconv.Atoi(strings.TrimSpace(m.x.Value()))
	y, errY := strconv.Atoi(strings.TrimSpace(m.y.Value()))
	if errW != nil || errH != nil || errX != nil || errY != nil ||
		w <= 0 || h <= 0 || x < 0 || y < 0 {
		m.err = "width and height must be positive; x and y must be zero or positive"
		return m, nil
	}
	if m.srcW > 0 && m.srcH > 0 {
		if w > m.srcW || h > m.srcH {
			m.err = fmt.Sprintf("crop window %dx%d exceeds the source size %dx%d", w, h, m.srcW, m.srcH)
			return m, nil
		}
		// Clamp the window back into the frame instead of failing inside
		// ffmpeg; the corrected values are written back so the user sees
		// exactly what will run.
		if x+w > m.srcW {
			x = m.srcW - w
			m.x.SetValue(strconv.Itoa(x))
		}
		if y+h > m.srcH {
			y = m.srcH - h
			m.y.SetValue(strconv.Itoa(y))
		}
	}
	m.cropW, m.cropH, m.cropX, m.cropY = w, h, x, y
	m.err = ""
	return m.beginOutput()
}

func (m *transformWizard) beginOutput() (tea.Model, tea.Cmd) {
	switch m.mode {
	case "rotate90":
		m.out.SetValue(ffx.RotateOutputName(m.filePath, 90))
	case "rotate180":
		m.out.SetValue(ffx.RotateOutputName(m.filePath, 180))
	case "rotate270":
		m.out.SetValue(ffx.RotateOutputName(m.filePath, 270))
	case "fliph":
		m.out.SetValue(ffx.FlipOutputName(m.filePath, "h"))
	case "flipv":
		m.out.SetValue(ffx.FlipOutputName(m.filePath, "v"))
	case "crop":
		m.out.SetValue(ffx.CropOutputName(m.filePath))
	}
	m.step = "output"
	m.blurCrop()
	m.out.Focus()
	return m, textinput.Blink
}

func (m *transformWizard) run() (tea.Model, tea.Cmd) {
	outName := strings.TrimSpace(m.out.Value())
	if outName == "" {
		m.err = "output file is required"
		return m, nil
	}
	m.err = ""
	outPath := resolveOutputPath(filepath.Dir(m.filePath), outName)
	if m.guard.shouldWarn(outPath) {
		return m, nil
	}
	var cmd ffx.Cmd
	title := ""
	switch m.mode {
	case "rotate90":
		cmd = ffx.BuildRotateCmd(m.filePath, 90, outPath, m.hdr.props)
		title = "Rotating video…"
	case "rotate180":
		cmd = ffx.BuildRotateCmd(m.filePath, 180, outPath, m.hdr.props)
		title = "Rotating video…"
	case "rotate270":
		cmd = ffx.BuildRotateCmd(m.filePath, 270, outPath, m.hdr.props)
		title = "Rotating video…"
	case "fliph":
		cmd = ffx.BuildFlipCmd(m.filePath, "h", outPath, m.hdr.props)
		title = "Flipping video…"
	case "flipv":
		cmd = ffx.BuildFlipCmd(m.filePath, "v", outPath, m.hdr.props)
		title = "Flipping video…"
	case "crop":
		cmd = ffx.BuildCropCmd(m.filePath, m.cropX, m.cropY, m.cropW, m.cropH, outPath, m.hdr.props)
		title = "Cropping video…"
	}
	return m, push(newExecScreen(m.cfg, title, execx.Cmd{Name: "ffmpeg", Args: cmd.Args}))
}

func (m *transformWizard) refocusCrop() {
	m.blurCrop()
	switch m.focus {
	case 0:
		m.width.Focus()
	case 1:
		m.height.Focus()
	case 2:
		m.x.Focus()
	case 3:
		m.y.Focus()
	}
}

func (m *transformWizard) blurCrop() {
	m.width.Blur()
	m.height.Blur()
	m.x.Blur()
	m.y.Blur()
}

func (m *transformWizard) View() string {
	switch m.step {
	case "probing":
		what := "Checking source color format…"
		if m.mode == "crop" {
			what = "Reading video dimensions…"
		}
		return m.style.Render("Transform Video\n\n" +
			m.spin.View() + " " + what + "\n\n" +
			lipgloss.NewStyle().Foreground(lipgloss.Color("241")).Render("Esc to go back"))
	case "crop":
		errLine := renderErrLine(m.err)
		return m.style.Render("Crop Video\n\n" +
			"Width:\n" + m.width.View() + "\n\n" +
			"Height:\n" + m.height.View() + "\n\n" +
			"X:\n" + m.x.View() + "\n\n" +
			"Y:\n" + m.y.View() + "\n" +
			errLine + "\n\n" +
			lipgloss.NewStyle().Foreground(lipgloss.Color("241")).Render(
				"Tab to switch fields • Enter to continue • Esc to go back • q to quit"))
	case "output":
		errLine := renderErrLine(m.err)
		warnLine := renderWarnLine(m.guard)
		noteLine := ""
		if m.hdr.note != "" {
			noteLine = "\n" + lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Render(m.hdr.note)
		}
		return m.style.Render("Output file:\n\n" + m.out.View() + errLine + warnLine + noteLine + "\n\nEnter to run • Esc to go back • q to quit")
	default:
		return m.style.Render(m.modeList.View() + "\n" + lipgloss.NewStyle().Foreground(lipgloss.Color("241")).Render(
			"Enter to select • Esc to go back • q to quit"))
	}
}
