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
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/fsx8/ffwiz/internal/execx"
	ffx "github.com/fsx8/ffwiz/internal/ffmpeg"
)

type effectsAudioMsg struct {
	hasAudio     bool
	audioStreams int            // probed audio track count; every track must survive
	note         string         // HDR/10-bit note for the re-encoding ops; empty when N/A
	props        ffx.ColorProps // generator input for the HDR-preserving branch
	err          error
}

type effectsWizard struct {
	cfg      Config
	filePath string

	opList      list.Model
	factorList  list.Model
	factorInput textinput.Model
	out         textinput.Model
	guard       overwriteGuard

	op           string // "speed" | "reverse" | "mute"
	step         string // "op" | "factor" | "custom" | "probing" | "output"
	factor       float64
	hasAudio     bool
	audioStreams int
	hdrNote      string
	props        ffx.ColorProps // color props for the HDR-preserving generators
	fromCustom   bool
	probing      bool
	spin         spinner.Model

	probeCancel context.CancelFunc // cancels the in-flight probe on ctrl+c

	err   string
	style lipgloss.Style
}

var effectsOps = []list.Item{
	simpleItem{value: "Change Speed…"},
	simpleItem{value: "Reverse Video"},
	simpleItem{value: "Mute Audio"},
}

var effectsOpKeys = map[string]string{
	"Change Speed…": "speed",
	"Reverse Video": "reverse",
	"Mute Audio":    "mute",
}

var effectsFactors = []list.Item{
	simpleItem{value: "0.25x"},
	simpleItem{value: "0.5x"},
	simpleItem{value: "0.75x"},
	simpleItem{value: "1.25x"},
	simpleItem{value: "1.5x"},
	simpleItem{value: "2x"},
	simpleItem{value: "Custom…"},
}

func newEffectsWizard(cfg Config, filePath string) *effectsWizard {
	ol := list.New(effectsOps, list.NewDefaultDelegate(), 0, 0)
	ol.Title = "Change speed, reverse, or mute"
	ol.SetFilteringEnabled(false)
	ol.DisableQuitKeybindings()

	fl := list.New(effectsFactors, list.NewDefaultDelegate(), 0, 0)
	fl.Title = "Select playback speed"
	fl.SetFilteringEnabled(false)
	fl.DisableQuitKeybindings()

	sp := spinner.New()
	sp.Spinner = spinner.Dot

	return &effectsWizard{
		cfg:         cfg,
		filePath:    filePath,
		opList:      ol,
		factorList:  fl,
		factorInput: newShortInput("0.25-4.0"),
		out:         newPathInput(),
		step:        "op",
		spin:        sp,
		style:       lipgloss.NewStyle().Padding(1, 2),
	}
}

func (m *effectsWizard) Init() tea.Cmd { return nil }

func (m *effectsWizard) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.opList.SetSize(dim(msg.Width, 4), dim(msg.Height, 6))
		m.factorList.SetSize(dim(msg.Width, 4), dim(msg.Height, 6))
	case effectsAudioMsg:
		return m.applyProbe(msg)
	case spinner.TickMsg:
		if m.step == "probing" && m.probing {
			var cmd tea.Cmd
			m.spin, cmd = m.spin.Update(msg)
			return m, cmd
		}
	case tea.KeyMsg:
		typing := (m.step == "output" && textInputFocused(m.out)) ||
			(m.step == "custom" && textInputFocused(m.factorInput))
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
				if m.op == "speed" {
					if m.fromCustom {
						m.step = "custom"
						m.factorInput.Focus()
						return m, textinput.Blink
					}
					m.step = "factor"
					return m, nil
				}
				m.step = "op"
				return m, nil
			case "custom":
				m.err = ""
				m.factorInput.Blur()
				m.step = "factor"
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
				if m.op == "speed" {
					m.step = "factor"
				} else {
					m.step = "op"
				}
				return m, nil
			case "factor":
				m.err = ""
				m.step = "op"
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
			case "op":
				return m.selectOp()
			case "factor":
				return m.selectFactor()
			case "custom":
				return m.applyCustom()
			case "output":
				return m.run()
			}
		}
		if m.step == "probing" {
			return m, nil
		}
	}

	var cmd tea.Cmd
	switch m.step {
	case "custom":
		m.factorInput, cmd = m.factorInput.Update(msg)
	case "output":
		m.out, cmd = m.out.Update(msg)
	case "factor":
		m.factorList, cmd = m.factorList.Update(msg)
	default:
		m.opList, cmd = m.opList.Update(msg)
	}
	return m, cmd
}

func (m *effectsWizard) selectOp() (tea.Model, tea.Cmd) {
	fi, ok := m.opList.SelectedItem().(simpleItem)
	if !ok {
		return m, nil
	}
	op, ok := effectsOpKeys[fi.value]
	if !ok {
		return m, nil
	}
	m.op = op
	switch op {
	case "speed":
		m.step = "factor"
		return m, nil
	case "reverse":
		// reverse re-encodes, so it needs the audio flag and color warning.
		m.step = "probing"
		m.probing = true
		return m, tea.Batch(m.spin.Tick, m.probeCmd())
	default:
		// Mute is a lossless stream copy: no probe needed at all.
		return m.beginOutput()
	}
}

func (m *effectsWizard) selectFactor() (tea.Model, tea.Cmd) {
	fi, ok := m.factorList.SelectedItem().(simpleItem)
	if !ok {
		return m, nil
	}
	if fi.value == "Custom…" {
		m.step = "custom"
		m.factorInput.Focus()
		return m, textinput.Blink
	}
	f, err := strconv.ParseFloat(strings.TrimSuffix(fi.value, "x"), 64)
	if err != nil {
		return m, nil
	}
	m.fromCustom = false
	return m.beginSpeed(f)
}

func (m *effectsWizard) applyCustom() (tea.Model, tea.Cmd) {
	f, err := strconv.ParseFloat(strings.TrimSpace(m.factorInput.Value()), 64)
	if err != nil || f < 0.25 || f > 4.0 {
		m.err = "speed factor must be a number between 0.25 and 4.0"
		return m, nil
	}
	m.err = ""
	m.fromCustom = true
	return m.beginSpeed(f)
}

func (m *effectsWizard) beginSpeed(f float64) (tea.Model, tea.Cmd) {
	m.factor = f
	m.factorInput.Blur()
	m.step = "probing"
	m.probing = true
	return m, tea.Batch(m.spin.Tick, m.probeCmd())
}

// probeCmd runs a single ffprobe off the UI thread to learn whether the
// file has audio (speed/reverse re-encode audio only when present), how
// many audio tracks it carries (every one of them must survive the
// filter_complex), and the source color format (both ops re-encode; the
// HDR-preserving branch and its note come from the same probe).
func (m *effectsWizard) probeCmd() tea.Cmd {
	prober := m.cfg.Prober
	path := m.filePath
	if prober == nil {
		return func() tea.Msg { return effectsAudioMsg{err: errors.New("no prober available")} }
	}
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	m.probeCancel = cancel
	return func() tea.Msg {
		defer cancel()
		res, err := prober.Probe(ctx, path)
		if err != nil {
			return effectsAudioMsg{err: err}
		}
		note := ""
		props := ffx.ColorProps{}
		if s := res.FirstVideo(); s != nil {
			note = hdrPreserveNoteFor(*s)
			props = colorProps(*s)
		}
		return effectsAudioMsg{
			hasAudio:     res.HasAudio(),
			audioStreams: len(res.StreamsOfType("audio")),
			note:         note,
			props:        props,
		}
	}
}

func (m *effectsWizard) applyProbe(msg effectsAudioMsg) (tea.Model, tea.Cmd) {
	if m.step != "probing" || !m.probing {
		return m, nil
	}
	m.probing = false
	if msg.err != nil {
		m.err = msg.err.Error()
		if m.op == "speed" {
			m.step = "factor"
		} else {
			m.step = "op"
		}
		return m, nil
	}
	m.hasAudio = msg.hasAudio
	m.audioStreams = msg.audioStreams
	m.hdrNote = msg.note
	m.props = msg.props
	return m.beginOutput()
}

func (m *effectsWizard) beginOutput() (tea.Model, tea.Cmd) {
	switch m.op {
	case "speed":
		m.out.SetValue(ffx.SpeedOutputName(m.filePath, m.factor))
	case "reverse":
		m.out.SetValue(ffx.ReverseOutputName(m.filePath))
	default:
		m.out.SetValue(ffx.MuteOutputName(m.filePath))
	}
	m.step = "output"
	m.out.Focus()
	return m, textinput.Blink
}

func (m *effectsWizard) run() (tea.Model, tea.Cmd) {
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
	switch m.op {
	case "speed":
		cmd = ffx.BuildSpeedCmd(m.filePath, m.factor, m.hasAudio, m.audioStreams, outPath, m.props)
		title = "Changing speed…"
	case "reverse":
		cmd = ffx.BuildReverseCmd(m.filePath, m.hasAudio, m.audioStreams, outPath, m.props)
		title = "Reversing video…"
	default:
		cmd = ffx.BuildMuteCmd(m.filePath, outPath)
		title = "Muting audio…"
	}
	return m, push(newExecScreen(m.cfg, title, execx.Cmd{Name: "ffmpeg", Args: cmd.Args}))
}

func (m *effectsWizard) View() string {
	errLine := renderErrLine(m.err)
	switch m.step {
	case "custom":
		return m.style.Render("Change Speed\n\n" +
			"Speed factor (0.25-4.0):\n" + m.factorInput.View() + "\n" +
			errLine + "\n\n" +
			lipgloss.NewStyle().Foreground(lipgloss.Color("241")).Render(
				"Enter to continue • Esc to go back • q to quit"))
	case "probing":
		return m.style.Render(m.spin.View() + " Checking for audio…\n\n" +
			lipgloss.NewStyle().Foreground(lipgloss.Color("241")).Render("Esc to go back"))
	case "output":
		warnLine := renderWarnLine(m.guard)
		noteLine := ""
		if m.hdrNote != "" {
			noteLine = "\n" + lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Render(m.hdrNote)
		}
		return m.style.Render("Output file:\n\n" + m.out.View() + errLine + warnLine + noteLine + "\n\nEnter to run • Esc to go back • q to quit")
	case "factor":
		return m.style.Render(m.factorList.View() + "\n" + lipgloss.NewStyle().Foreground(lipgloss.Color("241")).Render(
			"Enter to select • Esc to go back • q to quit"))
	default:
		return m.style.Render(m.opList.View() + "\n" + lipgloss.NewStyle().Foreground(lipgloss.Color("241")).Render(
			"Enter to select • Esc to go back • q to quit"))
	}
}
