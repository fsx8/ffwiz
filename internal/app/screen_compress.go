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

type compressWizard struct {
	cfg      Config
	filePath string

	qualityList list.Model
	speedList   list.Model
	crf         textinput.Model
	out         textinput.Model
	guard       overwriteGuard

	step   string // "quality" | "custom" | "speed" | "probing" | "output"
	crfV   int
	preset string
	// fromCustom tracks whether crfV came from the custom field, so Esc
	// from the output step can restore it (presets go back to "speed").
	fromCustom bool
	hdr        hdrWarner
	spin       spinner.Model
	err        string
	style      lipgloss.Style
}

var compressQualities = []list.Item{
	simpleItem{value: "High (CRF 18)"},
	simpleItem{value: "Medium (CRF 23)"},
	simpleItem{value: "Good (CRF 26)"},
	simpleItem{value: "Low (CRF 28)"},
	simpleItem{value: "Extreme (CRF 34)"},
	simpleItem{value: "Custom CRF…"},
}

var compressQualityCRFs = map[string]int{
	"High (CRF 18)":    18,
	"Medium (CRF 23)":  23,
	"Good (CRF 26)":    26,
	"Low (CRF 28)":     28,
	"Extreme (CRF 34)": 34,
}

var compressSpeeds = []list.Item{
	simpleItem{value: "ultrafast"},
	simpleItem{value: "superfast"},
	simpleItem{value: "veryfast"},
	simpleItem{value: "faster"},
	simpleItem{value: "fast"},
	simpleItem{value: "medium"},
	simpleItem{value: "slow"},
	simpleItem{value: "slower"},
}

func isCompressSpeed(name string) bool {
	switch name {
	case "ultrafast", "superfast", "veryfast", "faster", "fast", "medium", "slow", "slower":
		return true
	default:
		return false
	}
}

func newCompressWizard(cfg Config, filePath string) *compressWizard {
	ql := list.New(compressQualities, list.NewDefaultDelegate(), 0, 0)
	ql.Title = "Select compression quality"
	ql.SetFilteringEnabled(false)
	ql.DisableQuitKeybindings()

	sl := list.New(compressSpeeds, list.NewDefaultDelegate(), 0, 0)
	sl.Title = "Select encoding speed"
	sl.SetFilteringEnabled(false)
	sl.DisableQuitKeybindings()
	sl.Select(5) // medium

	sp := spinner.New()
	sp.Spinner = spinner.Dot

	return &compressWizard{
		cfg:         cfg,
		filePath:    filePath,
		qualityList: ql,
		speedList:   sl,
		crf:         newShortInput("0-51"),
		out:         newPathInput(),
		step:        "quality",
		spin:        sp,
		style:       lipgloss.NewStyle().Padding(1, 2),
	}
}

func (m *compressWizard) Init() tea.Cmd { return nil }

func (m *compressWizard) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.qualityList.SetSize(dim(msg.Width, 4), dim(msg.Height, 6))
		m.speedList.SetSize(dim(msg.Width, 4), dim(msg.Height, 6))
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
			(m.step == "custom" && textInputFocused(m.crf))
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
					m.crf.Focus()
					return m, textinput.Blink
				}
				m.step = "speed"
				return m, nil
			case "probing":
				m.err = ""
				m.step = "speed"
				return m, nil
			case "speed":
				m.err = ""
				m.step = "quality"
				return m, nil
			case "custom":
				m.err = ""
				m.crf.Blur()
				m.step = "quality"
				return m, nil
			default:
				m.guard.armedFor = ""
				m.err = ""
				return m, pop()
			}
		case "enter":
			switch m.step {
			case "quality":
				return m.selectQuality()
			case "custom":
				return m.applyCustom()
			case "speed":
				return m.selectSpeed()
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
		m.crf, cmd = m.crf.Update(msg)
	case "output":
		m.out, cmd = m.out.Update(msg)
	case "speed":
		m.speedList, cmd = m.speedList.Update(msg)
	default:
		m.qualityList, cmd = m.qualityList.Update(msg)
	}
	return m, cmd
}

func (m *compressWizard) selectQuality() (tea.Model, tea.Cmd) {
	fi, ok := m.qualityList.SelectedItem().(simpleItem)
	if !ok {
		return m, nil
	}
	if fi.value == "Custom CRF…" {
		m.step = "custom"
		m.crf.Focus()
		return m, textinput.Blink
	}
	crf, ok := compressQualityCRFs[fi.value]
	if !ok {
		return m, nil
	}
	m.fromCustom = false
	return m.beginSpeed(crf)
}

func (m *compressWizard) applyCustom() (tea.Model, tea.Cmd) {
	v, err := strconv.Atoi(strings.TrimSpace(m.crf.Value()))
	if err != nil || v < 0 || v > 51 {
		m.err = "CRF must be an integer between 0 and 51"
		return m, nil
	}
	m.err = ""
	m.fromCustom = true
	return m.beginSpeed(v)
}

func (m *compressWizard) beginSpeed(crf int) (tea.Model, tea.Cmd) {
	m.crfV = crf
	m.crf.Blur()
	m.step = "speed"
	return m, nil
}

func (m *compressWizard) selectSpeed() (tea.Model, tea.Cmd) {
	fi, ok := m.speedList.SelectedItem().(simpleItem)
	if !ok || !isCompressSpeed(fi.value) {
		return m, nil
	}
	m.preset = fi.value
	// Compression re-encodes the video, so check the source color format:
	// the probe decides between the HDR-preserving 10-bit branch and the
	// historical 8-bit command, and the note tells the user which one runs.
	m.step = "probing"
	return m, tea.Batch(m.spin.Tick, m.hdr.begin(m.cfg.Prober, m.filePath, hdrPreserveNoteFor))
}

func (m *compressWizard) beginOutput() (tea.Model, tea.Cmd) {
	m.out.SetValue(ffx.CompressOutputName(m.filePath, m.crfV))
	m.step = "output"
	m.out.Focus()
	return m, textinput.Blink
}

func (m *compressWizard) run() (tea.Model, tea.Cmd) {
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
	cmd := ffx.BuildCompressCmd(m.filePath, m.crfV, m.preset, outPath, m.hdr.props)
	return m, push(newExecScreen(m.cfg, "Compressing video…", execx.Cmd{Name: "ffmpeg", Args: cmd.Args}))
}

func (m *compressWizard) View() string {
	errLine := renderErrLine(m.err)
	switch m.step {
	case "custom":
		return m.style.Render("Compress Video\n\n" +
			"CRF (0-51, lower is better quality):\n" + m.crf.View() + "\n" +
			errLine + "\n\n" +
			lipgloss.NewStyle().Foreground(lipgloss.Color("241")).Render(
				"Enter to continue • Esc to go back • q to quit"))
	case "output":
		warnLine := renderWarnLine(m.guard)
		noteLine := ""
		if m.hdr.note != "" {
			noteLine = "\n" + lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Render(m.hdr.note)
		}
		return m.style.Render("Output file:\n\n" + m.out.View() + errLine + warnLine + noteLine + "\n\nEnter to run • Esc to go back • q to quit")
	case "probing":
		return m.style.Render(m.spin.View() + " Checking source color format…\n\n" +
			lipgloss.NewStyle().Foreground(lipgloss.Color("241")).Render("Esc to go back"))
	case "speed":
		return m.style.Render(m.speedList.View() + "\n" + lipgloss.NewStyle().Foreground(lipgloss.Color("241")).Render(
			"Enter to select • Esc to go back • q to quit"))
	default:
		return m.style.Render(m.qualityList.View() + "\n" + lipgloss.NewStyle().Foreground(lipgloss.Color("241")).Render(
			"Enter to select • Esc to go back • q to quit"))
	}
}
