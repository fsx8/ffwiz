package app

import (
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

type resizeWizard struct {
	cfg      Config
	filePath string

	presetList list.Model
	width      textinput.Model
	height     textinput.Model
	out        textinput.Model
	guard      overwriteGuard

	focus int // focused field on the custom step: 0 = width, 1 = height

	step       string // "preset" | "custom" | "probing" | "output"
	outWidth   int
	outHeight  int
	label      string
	fromCustom bool
	hdr        hdrWarner
	spin       spinner.Model
	err        string
	style      lipgloss.Style
}

var resizePresets = []list.Item{
	simpleItem{value: "2160p"},
	simpleItem{value: "1080p"},
	simpleItem{value: "720p"},
	simpleItem{value: "480p"},
	simpleItem{value: "Custom…"},
}

var resizePresetHeights = map[string]int{
	"2160p": 2160,
	"1080p": 1080,
	"720p":  720,
	"480p":  480,
}

func newResizeWizard(cfg Config, filePath string) *resizeWizard {
	l := list.New(resizePresets, list.NewDefaultDelegate(), 0, 0)
	l.Title = "Select target resolution"
	l.SetFilteringEnabled(false)
	l.DisableQuitKeybindings()

	width := newShortInput("auto (aspect preserved)")
	height := newShortInput("auto (aspect preserved)")
	out := newPathInput()

	sp := spinner.New()
	sp.Spinner = spinner.Dot

	return &resizeWizard{
		cfg:        cfg,
		filePath:   filePath,
		presetList: l,
		width:      width,
		height:     height,
		out:        out,
		step:       "preset",
		spin:       sp,
		style:      lipgloss.NewStyle().Padding(1, 2),
	}
}

func (m *resizeWizard) Init() tea.Cmd { return nil }

func (m *resizeWizard) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.presetList.SetSize(dim(msg.Width, 4), dim(msg.Height, 6))
	case spinner.TickMsg:
		if m.step == "probing" {
			var cmd tea.Cmd
			m.spin, cmd = m.spin.Update(msg)
			return m, cmd
		}
	case hdrProbeMsg:
		if m.step == "probing" {
			m.hdr.apply(msg)
			return m.beginOutput()
		}
	case tea.KeyMsg:
		typing := (m.step == "output" && textInputFocused(m.out)) ||
			(m.step == "custom" && textInputFocused(m.width, m.height))
		switch msg.String() {
		case "q":
			if typing {
				break
			}
			return m, tea.Quit
		case "ctrl+c":
			m.hdr.cancelProbe()
			return m, tea.Quit
		case "esc":
			switch m.step {
			case "output":
				m.guard.armedFor = ""
				m.err = ""
				m.out.Blur()
				if m.fromCustom {
					m.step = "custom"
					m.refocusCustom()
					return m, textinput.Blink
				}
				m.step = "preset"
				return m, nil
			case "probing":
				m.err = ""
				if m.fromCustom {
					m.step = "custom"
					m.refocusCustom()
					return m, textinput.Blink
				}
				m.step = "preset"
				return m, nil
			case "custom":
				m.err = ""
				m.width.Blur()
				m.height.Blur()
				m.step = "preset"
				return m, nil
			default:
				m.guard.armedFor = ""
				m.err = ""
				return m, pop()
			}
		case "enter":
			switch m.step {
			case "preset":
				return m.selectPreset()
			case "custom":
				return m.applyCustom()
			case "output":
				return m.run()
			}
		case "tab", "down":
			if m.step == "custom" {
				m.focus = focusStep(m.focus, 2, 1)
				m.refocusCustom()
				return m, textinput.Blink
			}
		case "shift+tab", "up":
			if m.step == "custom" {
				m.focus = focusStep(m.focus, 2, -1)
				m.refocusCustom()
				return m, textinput.Blink
			}
		}
		if m.step == "probing" {
			return m, nil
		}
	}

	var cmd tea.Cmd
	switch m.step {
	case "custom":
		switch m.focus {
		case 0:
			m.width, cmd = m.width.Update(msg)
		case 1:
			m.height, cmd = m.height.Update(msg)
		}
	case "output":
		m.out, cmd = m.out.Update(msg)
	default:
		m.presetList, cmd = m.presetList.Update(msg)
	}
	return m, cmd
}

func (m *resizeWizard) selectPreset() (tea.Model, tea.Cmd) {
	fi, ok := m.presetList.SelectedItem().(simpleItem)
	if !ok {
		return m, nil
	}
	if fi.value == "Custom…" {
		m.step = "custom"
		m.focus = 0
		m.refocusCustom()
		return m, textinput.Blink
	}
	height, ok := resizePresetHeights[fi.value]
	if !ok {
		return m, nil
	}
	m.fromCustom = false
	return m.beginProbe(-2, height, fi.value)
}

func (m *resizeWizard) applyCustom() (tea.Model, tea.Cmd) {
	ws := strings.TrimSpace(m.width.Value())
	hs := strings.TrimSpace(m.height.Value())
	if ws == "" && hs == "" {
		m.err = "width or height is required (leave the other field empty to keep the aspect ratio)"
		return m, nil
	}
	width, height := -2, -2
	if ws != "" {
		w, err := strconv.Atoi(ws)
		if err != nil || w <= 0 {
			m.err = "width must be a positive integer"
			return m, nil
		}
		width = w
	}
	if hs != "" {
		h, err := strconv.Atoi(hs)
		if err != nil || h <= 0 {
			m.err = "height must be a positive integer"
			return m, nil
		}
		height = h
	}
	m.err = ""
	m.fromCustom = true
	return m.beginProbe(width, height, "resized")
}

// beginProbe stores the chosen dimensions and checks the source color
// format (resize re-encodes the video; the probe feeds the HDR-preserving
// branch and its note) before showing the output step.
func (m *resizeWizard) beginProbe(width, height int, label string) (tea.Model, tea.Cmd) {
	m.outWidth = width
	m.outHeight = height
	m.label = label
	m.step = "probing"
	m.width.Blur()
	m.height.Blur()
	return m, tea.Batch(m.spin.Tick, m.hdr.begin(m.cfg.Prober, m.filePath, hdrPreserveNoteFor))
}

func (m *resizeWizard) beginOutput() (tea.Model, tea.Cmd) {
	m.out.SetValue(ffx.ResizeOutputName(m.filePath, m.label))
	m.step = "output"
	m.out.Focus()
	return m, textinput.Blink
}

func (m *resizeWizard) run() (tea.Model, tea.Cmd) {
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
	cmd := ffx.BuildResizeCmd(m.filePath, m.outWidth, m.outHeight, outPath, m.hdr.props)
	return m, push(newExecScreen(m.cfg, "Resizing video…", execx.Cmd{Name: "ffmpeg", Args: cmd.Args}))
}

func (m *resizeWizard) refocusCustom() {
	m.width.Blur()
	m.height.Blur()
	switch m.focus {
	case 0:
		m.width.Focus()
	case 1:
		m.height.Focus()
	}
}

func (m *resizeWizard) View() string {
	errLine := renderErrLine(m.err)
	switch m.step {
	case "custom":
		return m.style.Render("Resize Video\n\n" +
			"Width:\n" + m.width.View() + "\n\n" +
			"Height:\n" + m.height.View() + "\n" +
			errLine + "\n\n" +
			lipgloss.NewStyle().Foreground(lipgloss.Color("241")).Render(
				"Leave one field empty to derive it from the aspect ratio (even values).\nTab to switch fields • Enter to continue • Esc to go back • q to quit"))
	case "probing":
		return m.style.Render(m.spin.View() + " Checking source color format…\n\n" +
			lipgloss.NewStyle().Foreground(lipgloss.Color("241")).Render("Esc to go back"))
	case "output":
		warnLine := renderWarnLine(m.guard)
		noteLine := ""
		if m.hdr.note != "" {
			noteLine = "\n" + lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Render(m.hdr.note)
		}
		return m.style.Render("Output file:\n\n" + m.out.View() + errLine + warnLine + noteLine + "\n\nEnter to run • Esc to go back • q to quit")
	default:
		return m.style.Render(m.presetList.View() + "\n" + lipgloss.NewStyle().Foreground(lipgloss.Color("241")).Render(
			"Enter to select • Esc to go back • q to quit"))
	}
}
