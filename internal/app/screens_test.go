package app

import (
	"bytes"
	"context"
	"errors"
	"log"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/fsx8/ffwiz/internal/execx"
	ffx "github.com/fsx8/ffwiz/internal/ffmpeg"
	"github.com/fsx8/ffwiz/internal/ffprobe"
)

func flagValue(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// --- test doubles ---

type fakeProber struct {
	results   map[string]*ffprobe.ProbeResult
	err       map[string]error
	keyframes map[string][]float64
}

func (f *fakeProber) Probe(_ context.Context, path string) (*ffprobe.ProbeResult, error) {
	if f.err != nil {
		if err, ok := f.err[path]; ok {
			return nil, err
		}
	}
	if res, ok := f.results[path]; ok {
		return res, nil
	}
	return &ffprobe.ProbeResult{}, nil
}

func (f *fakeProber) HasAudio(ctx context.Context, path string) (bool, error) {
	res, err := f.Probe(ctx, path)
	if err != nil {
		return false, err
	}
	for _, s := range res.Streams {
		if s.CodecType == "audio" {
			return true, nil
		}
	}
	return false, nil
}

func (f *fakeProber) Keyframes(_ context.Context, path string) ([]float64, error) {
	if kf, ok := f.keyframes[path]; ok {
		return kf, nil
	}
	return nil, nil
}

// blockingProber's Keyframes blocks until its context is cancelled, so a
// test can observe that Esc aborts the in-flight trim probe.
type blockingProber struct {
	started   chan struct{}
	cancelled chan struct{}
}

func (f *blockingProber) Probe(context.Context, string) (*ffprobe.ProbeResult, error) {
	return &ffprobe.ProbeResult{}, nil
}

func (f *blockingProber) HasAudio(ctx context.Context, path string) (bool, error) {
	res, err := f.Probe(ctx, path)
	if err != nil {
		return false, err
	}
	return res.HasAudio(), nil
}

func (f *blockingProber) Keyframes(ctx context.Context, _ string) ([]float64, error) {
	close(f.started)
	<-ctx.Done()
	close(f.cancelled)
	return nil, ctx.Err()
}

// blockingProbeProber's Probe blocks until its context is cancelled, so a
// test can observe that quit/ctrl+c aborts an in-flight wizard probe.
type blockingProbeProber struct {
	started   chan struct{}
	cancelled chan struct{}
}

func (f *blockingProbeProber) Probe(ctx context.Context, _ string) (*ffprobe.ProbeResult, error) {
	close(f.started)
	<-ctx.Done()
	close(f.cancelled)
	return nil, ctx.Err()
}

func (f *blockingProbeProber) HasAudio(ctx context.Context, path string) (bool, error) {
	res, err := f.Probe(ctx, path)
	if err != nil {
		return false, err
	}
	return res.HasAudio(), nil
}

func (f *blockingProbeProber) Keyframes(context.Context, string) ([]float64, error) {
	return nil, nil
}

type fakeRunner struct {
	err error
}

func (f fakeRunner) Run(_ context.Context, _ execx.Cmd) (string, string, error) {
	if f.err != nil {
		return "", "boom stderr", f.err
	}
	return "", "", nil
}

func (f fakeRunner) RunStreaming(_ context.Context, _ execx.Cmd, _, onStderr func(string)) (int, error) {
	if f.err != nil && onStderr != nil {
		onStderr("boom stderr")
		return 1, f.err
	}
	if onStderr != nil {
		onStderr("fake stderr line")
	}
	return 0, nil
}

func videoWithAudio(duration string) *ffprobe.ProbeResult {
	return &ffprobe.ProbeResult{
		Streams: []ffprobe.Stream{
			{Index: 0, CodecType: "video", CodecName: "h264", Width: 640, Height: 360, SampleAspectRatio: "1:1"},
			{Index: 1, CodecType: "audio", CodecName: "aac", SampleRate: "48000"},
		},
		Format: ffprobe.Format{Duration: duration},
	}
}

func audioOnly() *ffprobe.ProbeResult {
	return &ffprobe.ProbeResult{
		Streams: []ffprobe.Stream{{Index: 0, CodecType: "audio", CodecName: "mp3", SampleRate: "44100"}},
		Format:  ffprobe.Format{Duration: "60"},
	}
}

// --- helpers ---

func keyMsg(s string) tea.KeyMsg {
	switch s {
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case " ":
		return tea.KeyMsg{Type: tea.KeySpace}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "ctrl+c":
		return tea.KeyMsg{Type: tea.KeyCtrlC}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
}

// msgsOf runs a command (resolving batched commands) to inspect its messages.
func msgsOf(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	switch m := cmd().(type) {
	case tea.BatchMsg:
		var out []tea.Msg
		for _, c := range m {
			out = append(out, msgsOf(c)...)
		}
		return out
	default:
		return []tea.Msg{m}
	}
}

// pumpFiltering feeds a list command's messages back into the model, as the
// bubbletea runtime would: list filtering is asynchronous (the matches are
// delivered as a FilterMatchesMsg), so tests must pump it explicitly.
func pumpFiltering(t *testing.T, m tea.Model, cmd tea.Cmd) {
	t.Helper()
	for _, msg := range msgsOf(cmd) {
		m.Update(msg)
	}
}

func hasMsg[T any](msgs []tea.Msg) bool {
	for _, m := range msgs {
		if _, ok := m.(T); ok {
			return true
		}
	}
	return false
}

func writeFile(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// --- key routing ---

func TestTrimWizard_TypingQReachesInput(t *testing.T) {
	m := newTrimWizard(Config{}, "/tmp/x.mp4")
	model, cmd := m.Update(keyMsg("q"))
	tw, ok := model.(*trimWizard)
	if !ok {
		t.Fatalf("unexpected model %T", model)
	}
	if tw.start.Value() != "q" {
		t.Fatalf("'q' must reach the focused input, got %q", tw.start.Value())
	}
	if hasMsg[tea.QuitMsg](msgsOf(cmd)) {
		t.Fatal("typing 'q' must not quit the app")
	}
}

func TestTrimWizard_EnterValidatesTimes(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "clip.mp4")
	m := newTrimWizard(Config{}, in)
	m.start.SetValue("10")
	m.end.SetValue("5")

	_, cmd := m.Update(keyMsg("enter"))
	if m.err == "" {
		t.Fatal("expected a validation error for start >= end")
	}
	if hasMsg[pushMsg](msgsOf(cmd)) {
		t.Fatal("must not proceed to the exec screen on invalid times")
	}

	m.end.SetValue("20")
	_, cmd = m.Update(keyMsg("enter"))
	if m.err != "" {
		t.Fatalf("valid range rejected: %s", m.err)
	}
	if m.step != "snapping" {
		t.Fatalf("valid range must enter the keyframe-snapping step, got %q", m.step)
	}
	_, cmd = m.Update(trimKeyframesMsg{})
	if !hasMsg[pushMsg](msgsOf(cmd)) {
		t.Fatal("expected the exec screen to be pushed after keyframe probing")
	}
}

func TestTrimWizard_OverwriteNeedsSecondEnter(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "clip.mp4")
	writeFile(t, dir, "clip.mp4")
	writeFile(t, dir, "clip_trimmed.mp4") // existing output

	m := newTrimWizard(Config{}, in)
	m.start.SetValue("0")
	m.end.SetValue("5")

	_, cmd := m.Update(keyMsg("enter"))
	if hasMsg[pushMsg](msgsOf(cmd)) {
		t.Fatal("first Enter must not overwrite an existing file")
	}
	if m.guard.armedFor == "" {
		t.Fatal("expected an overwrite warning")
	}

	_, cmd = m.Update(keyMsg("enter"))
	if hasMsg[pushMsg](msgsOf(cmd)) {
		t.Fatal("second Enter starts keyframe probing, not the exec screen yet")
	}
	_, cmd = m.Update(trimKeyframesMsg{})
	if !hasMsg[pushMsg](msgsOf(cmd)) {
		t.Fatal("second Enter should confirm the overwrite and run")
	}
}

func execOfPush(t *testing.T, cmd tea.Cmd) *execModel {
	t.Helper()
	for _, msg := range msgsOf(cmd) {
		if p, ok := msg.(pushMsg); ok {
			em, ok := p.m.(*execModel)
			if !ok {
				t.Fatalf("pushed model is %T, want *execModel", p.m)
			}
			return em
		}
	}
	t.Fatal("no pushMsg produced")
	return nil
}

func TestTrimWizard_SnapsStartToPreviousKeyframe(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "clip.mp4")
	kf := []float64{0, 2, 4, 6, 8, 10}
	cfg := Config{Prober: &fakeProber{keyframes: map[string][]float64{in: kf}}}

	m := newTrimWizard(cfg, in)
	m.start.SetValue("5")
	m.end.SetValue("9")

	_, cmd := m.Update(keyMsg("enter"))
	if m.step != "snapping" {
		t.Fatalf("expected snapping step, got %q", m.step)
	}
	_, cmd = m.Update(trimKeyframesMsg{keyframes: kf})
	em := execOfPush(t, cmd)
	if got := flagValue(em.cmd.Args, "-ss"); got != "00:00:04" {
		t.Fatalf("snapped -ss = %q, want 00:00:04 (previous keyframe)", got)
	}
	if got := flagValue(em.cmd.Args, "-to"); got != "00:00:09" {
		t.Fatalf("-to = %q, want 00:00:09 (end unchanged)", got)
	}
	if !strings.Contains(em.title, "00:00:04") {
		t.Fatalf("exec title should surface the snapped start, got %q", em.title)
	}
}

func TestTrimWizard_WithoutKeyframesTrimsUnsnapped(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "clip.mp4")
	m := newTrimWizard(Config{}, in) // nil Prober: immediate empty keyframes
	m.start.SetValue("5")
	m.end.SetValue("9")

	_, cmd := m.Update(keyMsg("enter"))
	_, cmd = m.Update(trimKeyframesMsg{})
	em := execOfPush(t, cmd)
	if got := flagValue(em.cmd.Args, "-ss"); got != "00:00:05" {
		t.Fatalf("-ss = %q, want the unsnapped 00:00:05", got)
	}
	if strings.Contains(em.title, "lossless cut") {
		t.Fatalf("title should not claim snapping, got %q", em.title)
	}
}

// Esc during keyframe probing must abort the probe: a result arriving
// afterwards is stale and must not push a trim the user cancelled.
func TestTrimWizard_LateKeyframesAfterEscDoNotLaunchTrim(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "clip.mp4")
	cfg := Config{Prober: &fakeProber{keyframes: map[string][]float64{in: {0, 2, 4}}}}
	m := newTrimWizard(cfg, in)
	m.start.SetValue("2")
	m.end.SetValue("8")

	m.Update(keyMsg("enter")) // -> snapping
	if m.probeCancel == nil {
		t.Fatal("starting the snapping step must store a probe cancel func")
	}
	m.Update(keyMsg("esc")) // user aborts while probing
	if m.step != "form" {
		t.Fatalf("esc during snapping must return to the form, got %q", m.step)
	}
	if m.probeCancel != nil {
		t.Fatal("esc during snapping must clear the stored cancel func")
	}

	_, cmd := m.Update(trimKeyframesMsg{keyframes: []float64{0, 2}, dur: 20})
	if hasMsg[pushMsg](msgsOf(cmd)) {
		t.Fatal("a stale probe result after Esc must not launch the trim")
	}
	if m.step != "form" {
		t.Fatalf("a stale probe result must leave the wizard on the form, got %q", m.step)
	}
}

func TestTrimWizard_EscDuringSnappingCancelsProbe(t *testing.T) {
	p := &blockingProber{started: make(chan struct{}), cancelled: make(chan struct{})}
	m := newTrimWizard(Config{Prober: p}, "/tmp/clip.mp4")
	m.start.SetValue("1")
	m.end.SetValue("4")

	_, cmd := m.Update(keyMsg("enter")) // -> snapping
	done := make(chan struct{})
	go func() {
		defer close(done)
		msgsOf(cmd) // runs the batch, including the blocking probe
	}()
	<-p.started

	m.Update(keyMsg("esc"))
	select {
	case <-p.cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("esc during snapping must cancel the in-flight keyframe probe")
	}
	<-done
}

// q during probing must cancel the in-flight probe before quitting, so no
// ffprobe (worst case a keyframe scan) keeps running behind a dead UI.
func TestJoinWizard_QuitDuringProbingCancelsProbe(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.mp4")
	writeFile(t, dir, "b.mp4")
	p := &blockingProbeProber{started: make(chan struct{}), cancelled: make(chan struct{})}
	m := newJoinWizard(Config{Prober: p}, dir)

	m.Update(keyMsg("down"))
	m.Update(keyMsg(" "))
	m.Update(keyMsg("down"))
	m.Update(keyMsg(" "))
	m.Update(keyMsg("enter"))           // -> output
	_, cmd := m.Update(keyMsg("enter")) // -> probing
	done := make(chan struct{})
	go func() {
		defer close(done)
		msgsOf(cmd) // runs the batch, including the blocking probe
	}()
	<-p.started

	_, cmd = m.Update(keyMsg("q"))
	if !hasMsg[tea.QuitMsg](msgsOf(cmd)) {
		t.Fatal("q during probing must quit")
	}
	select {
	case <-p.cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("q during probing must cancel the in-flight probe")
	}
	<-done
}

func TestTrimWizard_CtrlCDuringSnappingCancelsProbe(t *testing.T) {
	p := &blockingProber{started: make(chan struct{}), cancelled: make(chan struct{})}
	m := newTrimWizard(Config{Prober: p}, "/tmp/clip.mp4")
	m.start.SetValue("1")
	m.end.SetValue("4")

	_, cmd := m.Update(keyMsg("enter")) // -> snapping
	done := make(chan struct{})
	go func() {
		defer close(done)
		msgsOf(cmd) // runs the batch, including the blocking probe
	}()
	<-p.started

	m.Update(keyMsg("ctrl+c"))
	select {
	case <-p.cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("ctrl+c during snapping must cancel the in-flight keyframe probe")
	}
	<-done
}

func TestFilePicker_ManualModeQTypes(t *testing.T) {
	m := newFilePicker(Config{}, t.TempDir())
	m.mode = "manual"
	m.input.Focus()

	model, cmd := m.Update(keyMsg("q"))
	fp, ok := model.(*filePickerModel)
	if !ok {
		t.Fatalf("unexpected model %T", model)
	}
	if fp.mode != "manual" || fp.input.Value() != "q" {
		t.Fatalf("'q' must reach the path input (mode=%q value=%q)", fp.mode, fp.input.Value())
	}
	if hasMsg[tea.QuitMsg](msgsOf(cmd)) {
		t.Fatal("typing 'q' must not quit the app")
	}
}

func TestFilePicker_EscWhileFilteringClearsFilter(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.mp4")
	writeFile(t, dir, "b.mp4")
	m := newFilePicker(Config{}, dir)

	m.Update(keyMsg("/"))
	if m.list.FilterState() == 0 { // list.Unfiltered
		t.Fatal("expected filter to be active after '/'")
	}

	_, cmd := m.Update(keyMsg("esc"))
	if m.list.FilterState() != 0 {
		t.Fatal("esc should clear the filter, not pop the screen")
	}
	if hasMsg[popMsg](msgsOf(cmd)) {
		t.Fatal("esc must not pop the screen while filtering")
	}

	// A second esc (no filter) pops.
	_, cmd = m.Update(keyMsg("esc"))
	if !hasMsg[popMsg](msgsOf(cmd)) {
		t.Fatal("esc without filter should pop the screen")
	}
}

func TestJoinWizard_SpaceWhileFilteringGoesToFilter(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a part1.mp4")
	writeFile(t, dir, "b part2.mp4")
	m := newJoinWizard(Config{}, dir)

	m.Update(keyMsg("/"))
	if m.list.FilterState() == 0 {
		t.Fatal("expected filter to be active after '/'")
	}

	m.Update(keyMsg(" "))
	for _, it := range m.list.Items() {
		if ji, ok := it.(*joinItem); ok && ji.selected {
			t.Fatal("space must type into the filter, not toggle selection")
		}
	}
}

func TestTracksWizard_TypingQReachesOutputInput(t *testing.T) {
	m := newTracksWizard(Config{}, "x.mp4")
	m.loading = false
	m.tracks = []trackView{{Track: ffx.Track{Index: 0, Type: ffx.TrackVideo}}}
	m.actions[0] = ffx.TrackActionInfo{Action: ffx.ActionKeep}
	m.step = "output"
	m.out.Focus()

	model, cmd := m.Update(keyMsg("q"))
	tw, ok := model.(*tracksWizard)
	if !ok {
		t.Fatalf("unexpected model %T", model)
	}
	if tw.out.Value() != "q" {
		t.Fatalf("'q' must reach the output input, got %q", tw.out.Value())
	}
	if hasMsg[tea.QuitMsg](msgsOf(cmd)) {
		t.Fatal("typing 'q' must not quit the app")
	}
}

func TestExtractAudio_EscFromOutputReturnsToFormat(t *testing.T) {
	m := newExtractAudioWizard(Config{}, "x.mp4")
	m.loading = false
	m.hasAudio = true
	m.mode = "output"

	_, cmd := m.Update(keyMsg("esc"))
	if m.mode != "format" {
		t.Fatalf("esc from the output step must return to the format step, got %q", m.mode)
	}
	if hasMsg[popMsg](msgsOf(cmd)) {
		t.Fatal("esc from the output step must not pop the whole wizard")
	}
}

// --- join wizard flow ---

func TestJoinWizard_FullFlowToConfirmAndRun(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.mp4")
	writeFile(t, dir, "b.mp4")
	a := filepath.Join(dir, "a.mp4")
	b := filepath.Join(dir, "b.mp4")

	cfg := Config{Prober: &fakeProber{results: map[string]*ffprobe.ProbeResult{
		a: videoWithAudio("10"),
		b: videoWithAudio("12"),
	}}}
	m := newJoinWizard(cfg, dir)

	// Skip the ".." navigation entry, then select both files in list order.
	m.Update(keyMsg("down"))
	m.Update(keyMsg(" "))
	m.Update(keyMsg("down"))
	m.Update(keyMsg(" "))
	m.Update(keyMsg("enter"))
	if m.step != "output" {
		t.Fatalf("expected output step, got %q", m.step)
	}

	// Enter output name -> probing starts.
	_, cmd := m.Update(keyMsg("enter"))
	if m.step != "probing" {
		t.Fatalf("expected probing step, got %q", m.step)
	}
	var probe joinProbeMsg
	for _, msg := range msgsOf(cmd) {
		if p, ok := msg.(joinProbeMsg); ok {
			probe = p
		}
	}
	if probe.err != nil {
		t.Fatalf("probe failed: %v", probe.err)
	}
	if len(probe.inputs) != 2 || !probe.inputs[0].HasAudio || probe.inputs[1].DurationSec != 12 {
		t.Fatalf("unexpected probe result: %+v", probe.inputs)
	}
	if probe.target.Width != 640 || probe.target.SampleRate != "48000" {
		t.Fatalf("unexpected target: %+v", probe.target)
	}

	// Probe done -> confirm screen with command preview.
	m.Update(probe)
	if m.step != "confirm" {
		t.Fatalf("expected confirm step, got %q", m.step)
	}

	// Enter runs the join.
	_, cmd = m.Update(keyMsg("enter"))
	if !hasMsg[pushMsg](msgsOf(cmd)) {
		t.Fatal("expected the exec screen to be pushed from confirm")
	}
}

func TestJoinWizard_ProbeErrorReturnsToSelect(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.mp4")
	writeFile(t, dir, "broken.mp4")
	cfg := Config{Prober: &fakeProber{
		err: map[string]error{filepath.Join(dir, "broken.mp4"): errors.New("ffprobe failed")},
	}}
	m := newJoinWizard(cfg, dir)

	m.Update(keyMsg("down")) // skip the ".." entry
	m.Update(keyMsg(" "))
	m.Update(keyMsg("down"))
	m.Update(keyMsg(" "))
	m.Update(keyMsg("enter"))
	_, cmd := m.Update(keyMsg("enter")) // -> probing
	var probe joinProbeMsg
	for _, msg := range msgsOf(cmd) {
		if p, ok := msg.(joinProbeMsg); ok {
			probe = p
		}
	}
	if probe.err == nil {
		t.Fatal("expected probe error for unreadable file")
	}
	m.Update(probe)
	if m.step != "select" || m.err == "" {
		t.Fatalf("expected error surfaced on select step, got step=%q err=%q", m.step, m.err)
	}
}

func TestJoinWizard_JoinOrderShownInListOrder(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "part2.mp4")
	writeFile(t, dir, "part10.mp4")
	writeFile(t, dir, "part1.mp4")
	m := newJoinWizard(Config{}, dir)

	// Cursor starts at the ".." entry; step past it to part1 (natural order).
	m.Update(keyMsg("down"))
	m.Update(keyMsg(" ")) // part1
	m.Update(keyMsg("down"))
	m.Update(keyMsg(" ")) // part2
	m.Update(keyMsg("down"))
	m.Update(keyMsg(" ")) // part10

	got := m.selectedPaths()
	want := []string{
		filepath.Join(dir, "part1.mp4"),
		filepath.Join(dir, "part2.mp4"),
		filepath.Join(dir, "part10.mp4"),
	}
	if len(got) != len(want) {
		t.Fatalf("selected: %#v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("join order must follow natural list order:\ngot  %#v\nwant %#v", got, want)
		}
	}
	// Order labels: part1 -> 1, part2 -> 2, part10 -> 3.
	items := m.list.Items()
	if ji := items[3].(*joinItem); ji.order != 3 {
		t.Fatalf("part10 should join at position 3, got %d", ji.order)
	}
}

// --- batch ---

func TestBatchRun_SkipsAudioOnlyFilesForVideoTarget(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "video.mp4")
	writeFile(t, dir, "song.mp3")
	cfg := Config{Prober: &fakeProber{results: map[string]*ffprobe.ProbeResult{
		filepath.Join(dir, "video.mp4"): videoWithAudio("10"),
		filepath.Join(dir, "song.mp3"):  audioOnly(),
	}}, Runner: fakeRunner{}}

	m := newBatchRun(cfg, dir, "mp4", ffx.QualityMedium)
	msg := m.runBatchCmd()()
	st, ok := msg.(batchStatusMsg)
	if !ok {
		t.Fatalf("unexpected msg %T", msg)
	}
	if st.ok != 1 || st.skipped != 1 || st.fail != 0 {
		t.Fatalf("ok=%d skipped=%d fail=%d; %+v", st.ok, st.skipped, st.fail, st)
	}
}

func TestBatchRun_ConvertsAllForAudioTarget(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "video.mp4")
	writeFile(t, dir, "song.flac") // lossless source, genuinely converted
	cfg := Config{Prober: &fakeProber{results: map[string]*ffprobe.ProbeResult{
		filepath.Join(dir, "video.mp4"): videoWithAudio("10"),
		filepath.Join(dir, "song.flac"): audioOnly(),
	}}, Runner: fakeRunner{}}

	m := newBatchRun(cfg, dir, "mp3", ffx.QualityMedium)
	st, ok := m.runBatchCmd()().(batchStatusMsg)
	if !ok {
		t.Fatal("unexpected msg")
	}
	if st.ok != 2 || st.skipped != 0 || st.fail != 0 {
		t.Fatalf("ok=%d skipped=%d fail=%d", st.ok, st.skipped, st.fail)
	}
}

func TestBatchRun_SkipsFilesAlreadyInTargetFormat(t *testing.T) {
	// mp3 -> mp3 at a fixed bitrate is a pure quality loss; same-extension
	// files are left alone unless an explicit video quality preset was
	// chosen (i.e. the user asked for a re-encode).
	dir := t.TempDir()
	writeFile(t, dir, "song.mp3")
	cfg := Config{Prober: &fakeProber{results: map[string]*ffprobe.ProbeResult{
		filepath.Join(dir, "song.mp3"): audioOnly(),
	}}, Runner: fakeRunner{}}

	m := newBatchRun(cfg, dir, "mp3", ffx.QualityMedium)
	st, ok := m.runBatchCmd()().(batchStatusMsg)
	if !ok {
		t.Fatal("unexpected msg")
	}
	if st.ok != 0 || st.skipped != 1 || st.fail != 0 {
		t.Fatalf("ok=%d skipped=%d fail=%d", st.ok, st.skipped, st.fail)
	}
	if !strings.Contains(st.last, "already mp3") {
		t.Fatalf("skip reason should mention the format, got %q", st.last)
	}
}

func TestBatchRun_SameExtensionStillConvertsWithQualityPreset(t *testing.T) {
	// An explicit quality preset for a video target is a deliberate
	// re-encode request, so mp4 -> mp4 (CRF 23) proceeds.
	dir := t.TempDir()
	writeFile(t, dir, "video.mp4")
	cfg := Config{Prober: &fakeProber{results: map[string]*ffprobe.ProbeResult{
		filepath.Join(dir, "video.mp4"): videoWithAudio("10"),
	}}, Runner: fakeRunner{}}

	m := newBatchRun(cfg, dir, "mp4", ffx.QualityMedium)
	st, ok := m.runBatchCmd()().(batchStatusMsg)
	if !ok {
		t.Fatal("unexpected msg")
	}
	if st.ok != 1 || st.skipped != 0 {
		t.Fatalf("ok=%d skipped=%d fail=%d", st.ok, st.skipped, st.fail)
	}
}

func TestBatchRun_CountsExistingOverwrites(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "video.mp4")
	writeFile(t, dir, "video_batch.mkv") // stale output from an earlier run
	cfg := Config{Prober: &fakeProber{results: map[string]*ffprobe.ProbeResult{
		filepath.Join(dir, "video.mp4"): videoWithAudio("10"),
	}}, Runner: fakeRunner{}}

	m := newBatchRun(cfg, dir, "mkv", ffx.QualitySame)
	if m.overwrites != 1 {
		t.Fatalf("overwrites = %d, want 1", m.overwrites)
	}
}

func TestBatchRun_FailuresAreLoggedWithStderr(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "video.mp4")
	var buf bytes.Buffer
	cfg := Config{
		Logger: log.New(&buf, "", 0),
		Prober: &fakeProber{results: map[string]*ffprobe.ProbeResult{filepath.Join(dir, "video.mp4"): videoWithAudio("10")}},
		Runner: fakeRunner{err: errors.New("exit status 3")},
	}

	m := newBatchRun(cfg, dir, "mp4", ffx.QualityMedium)
	st, ok := m.runBatchCmd()().(batchStatusMsg)
	if !ok {
		t.Fatal("unexpected msg")
	}
	if st.fail != 1 || st.ok != 0 {
		t.Fatalf("fail=%d ok=%d", st.fail, st.ok)
	}
	if !bytes.Contains(buf.Bytes(), []byte("boom stderr")) {
		t.Fatalf("ffmpeg stderr should be logged for diagnosis, log: %q", buf.String())
	}
}

// --- navigation ---

func TestRootPushPopNavigation(t *testing.T) {
	r, ok := New(Config{}).(*rootModel)
	if !ok {
		t.Fatalf("unexpected model %T", r)
	}
	if len(r.stack) != 1 {
		t.Fatalf("expected a single start screen, got %d", len(r.stack))
	}

	_, cmd := r.Update(pushMsg{m: newMainMenu(Config{})})
	if len(r.stack) != 2 {
		t.Fatalf("push must grow the stack, got %d", len(r.stack))
	}
	if hasMsg[pushMsg](msgsOf(cmd)) {
		t.Fatal("push handler should not re-emit push")
	}

	r.Update(popMsg{})
	if len(r.stack) != 1 {
		t.Fatalf("pop must shrink the stack, got %d", len(r.stack))
	}

	_, cmd = r.Update(popMsg{})
	if !hasMsg[tea.QuitMsg](msgsOf(cmd)) {
		t.Fatal("popping the last screen must quit")
	}
}

func TestRoot_CtrlCQuits(t *testing.T) {
	r, _ := New(Config{}).(*rootModel)
	_, cmd := r.Update(keyMsg("ctrl+c"))
	if !hasMsg[tea.QuitMsg](msgsOf(cmd)) {
		t.Fatal("ctrl+c must quit the program")
	}
}

// --- exec result classification ---

func TestExecScreen_CancelledVersusFailed(t *testing.T) {
	m := newExecScreen(Config{}, "test", execx.Cmd{Name: "true"})

	m.done = &execDoneMsg{exitCode: -1, err: errors.New("signal: killed")}
	if !m.wasCancelled() {
		t.Fatal("killed process (negative exit code) should read as cancelled")
	}

	m.done = &execDoneMsg{exitCode: 1, err: context.Canceled}
	if !m.wasCancelled() {
		t.Fatal("context.Canceled should read as cancelled")
	}

	m.done = &execDoneMsg{exitCode: 3, err: errors.New("command failed (exit 3)")}
	if m.wasCancelled() {
		t.Fatal("a real non-zero exit must not read as cancelled")
	}
}

// --- regression: tracks wizard after a failed probe (T1) ---

func TestTracksWizard_KeysAfterProbeErrorDoNotPanic(t *testing.T) {
	m := newTracksWizard(Config{Prober: &fakeProber{err: map[string]error{"x.mp4": errors.New("boom")}}}, "x.mp4")
	m.Update(tracksDoneMsg{err: errors.New("boom")})

	for _, k := range []string{"c", "r", "k", "up", "down", "enter"} {
		m.Update(keyMsg(k)) // must not panic even with zero tracks
	}
	if m.step != "tracks" {
		t.Fatalf("expected to stay on the tracks step after a probe error, got %q", m.step)
	}
}

// --- trim: keyframe snap must not push the start past the end (T3) ---

func TestTrimWizard_SnapPastEndRefusesCut(t *testing.T) {
	m := newTrimWizard(Config{}, "/tmp/x.mp4")
	m.start.SetValue("0")
	m.end.SetValue("0.5")

	m.Update(keyMsg("enter")) // -> snapping
	m.Update(trimKeyframesMsg{keyframes: []float64{3.0}})

	if m.step != "form" {
		t.Fatalf("expected to return to the form, got %q", m.step)
	}
	if m.err == "" {
		t.Fatal("expected an error for a cut emptied by keyframe snapping")
	}
}

// --- overwrite guard applies to the tracks confirm step too (T5) ---

func TestTracksWizard_ConfirmOverwriteNeedsSecondEnter(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "exists.mp4")
	in := filepath.Join(dir, "in.mp4")

	m := newTracksWizard(Config{}, in)
	m.loading = false
	m.tracks = []trackView{{Track: ffx.Track{Index: 0, Type: ffx.TrackVideo, Codec: "h264"}, Width: 640, Height: 360}}
	m.actions[0] = ffx.TrackActionInfo{Action: ffx.ActionKeep}

	m.Update(keyMsg("enter")) // tracks -> output
	m.out.SetValue(filepath.Join(dir, "exists.mp4"))
	m.Update(keyMsg("enter")) // output -> confirm
	if m.step != "confirm" {
		t.Fatalf("expected confirm step, got %q", m.step)
	}

	_, cmd := m.Update(keyMsg("enter"))
	if hasMsg[pushMsg](msgsOf(cmd)) {
		t.Fatal("first Enter must not overwrite an existing output")
	}
	if m.guard.armedFor == "" {
		t.Fatal("expected an armed overwrite warning")
	}

	_, cmd = m.Update(keyMsg("enter"))
	if !hasMsg[pushMsg](msgsOf(cmd)) {
		t.Fatal("second Enter should confirm the overwrite and run")
	}
}

// --- applied-filter semantics (T4) ---

func TestJoinWizard_AppliedFilterAllowsToggleAndEnter(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "alpha.mp4")
	writeFile(t, dir, "beta.mp4")
	m := newJoinWizard(Config{}, dir)

	m.Update(keyMsg("down"))        // skip ".." -> alpha
	m.Update(keyMsg("/"))           // filter mode
	_, cmd := m.Update(keyMsg("a")) // types "a": alpha matches
	pumpFiltering(t, m, cmd)
	m.Update(keyMsg("enter")) // apply the filter
	if m.list.FilterState() != list.FilterApplied {
		t.Fatalf("expected an applied filter, got %v", m.list.FilterState())
	}

	// While a filter is applied, Space must toggle and Enter must work.
	m.Update(keyMsg(" "))
	if sel := m.selectedPaths(); len(sel) != 1 || filepath.Base(sel[0]) != "alpha.mp4" {
		t.Fatalf("space must toggle the highlighted file under an applied filter: %#v", sel)
	}

	m.Update(keyMsg("esc")) // clears the filter instead of popping
	if m.list.FilterState() != list.Unfiltered {
		t.Fatalf("esc should clear an applied filter, got %v", m.list.FilterState())
	}

	// resetFiltering keeps the cursor, which now points at ".." again.
	m.Update(keyMsg("down")) // ".." -> alpha (still selected)
	m.Update(keyMsg("down")) // alpha -> beta
	m.Update(keyMsg(" "))
	m.Update(keyMsg("enter"))
	if m.step != "output" {
		t.Fatalf("enter must continue after the filter was cleared, got %q", m.step)
	}
}

func TestFilePicker_EscWithAppliedFilterClearsInsteadOfPopping(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.mp4")
	writeFile(t, dir, "b.mp4")
	m := newFilePicker(Config{}, dir)

	m.Update(keyMsg("/"))
	_, cmd := m.Update(keyMsg("b"))
	pumpFiltering(t, m, cmd)
	m.Update(keyMsg("enter")) // apply
	if m.list.FilterState() != list.FilterApplied {
		t.Fatalf("expected an applied filter, got %v", m.list.FilterState())
	}

	_, cmd = m.Update(keyMsg("esc"))
	if m.list.FilterState() != list.Unfiltered {
		t.Fatal("esc should clear an applied filter")
	}
	if hasMsg[popMsg](msgsOf(cmd)) {
		t.Fatal("esc must not pop the screen while a filter is applied")
	}
}

// --- directory navigation (T11) ---

func TestJoinWizard_DirectoryNavigation(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "season1")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, sub, "e1.mp4")
	m := newJoinWizard(Config{}, root)

	m.Update(keyMsg("down")) // ".." -> season1
	m.Update(keyMsg("enter"))
	if m.dir != sub {
		t.Fatalf("dir = %q, want %q", m.dir, sub)
	}
	found := false
	for _, it := range m.list.Items() {
		if ji, ok := it.(*joinItem); ok && ji.name == "e1.mp4" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected the subdirectory's video after navigating")
	}

	m.Update(keyMsg("esc"))
	if m.dir != root {
		t.Fatalf("esc should walk back up, dir = %q", m.dir)
	}
	_, cmd := m.Update(keyMsg("esc"))
	if !hasMsg[popMsg](msgsOf(cmd)) {
		t.Fatal("esc at the start directory should pop the screen")
	}
}

func TestBatchWizard_DirectoryNavigationThenFormat(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "movies")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	m := newBatchWizard(Config{}, root)
	if m.step != "dir" {
		t.Fatalf("expected the dir step first, got %q", m.step)
	}

	m.Update(keyMsg("down")) // ".." -> movies
	m.Update(keyMsg("enter"))
	if m.dir != sub {
		t.Fatalf("dir = %q, want %q", m.dir, sub)
	}

	// Items are [.., "Convert files in this directory"]: skip ".." and go.
	m.Update(keyMsg("down"))
	m.Update(keyMsg("enter"))
	if m.step != "format" {
		t.Fatalf("expected the format step after choosing the directory, got %q", m.step)
	}
	if m.dir != sub {
		t.Fatalf("chosen dir = %q, want %q", m.dir, sub)
	}
}

// A failed directory read must not leave a stale error behind once a
// directory can be read again (esc walks back up and refreshes).
func TestBatchWizard_ListErrClearsOnSuccessfulRefresh(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "movies")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	m := newBatchWizard(Config{}, sub)

	m.dir = filepath.Join(root, "does-not-exist")
	m.refreshDirs()
	if m.listErr == "" {
		t.Fatal("a failed directory read must set listErr")
	}

	m.Update(keyMsg("esc")) // walks back up to root and refreshes
	if m.listErr != "" {
		t.Fatalf("a successful refresh must clear listErr, got %q", m.listErr)
	}
}

func TestFilePicker_SurfaceReadErrors(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	m := newFilePicker(Config{}, missing)
	if m.err == "" {
		t.Fatal("an unreadable directory must surface an error, not show a silently empty list")
	}
	manual := false
	for _, it := range m.list.Items() {
		if fi, ok := it.(fileItem); ok && fi.kind == "manual" {
			manual = true
		}
	}
	if !manual {
		t.Fatal("manual path entry must remain available even on read errors")
	}
}

// --- join confirm: mixed frame rate note (T14) ---

func TestJoinWizard_FpsNoteOnMixedFrameRates(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.mp4")
	writeFile(t, dir, "b.mp4")
	resA := videoWithAudio("10")
	resA.Streams[0].RFrameRate = "30000/1001"
	resB := videoWithAudio("12")
	resB.Streams[0].RFrameRate = "24000/1001"
	cfg := Config{Prober: &fakeProber{results: map[string]*ffprobe.ProbeResult{
		filepath.Join(dir, "a.mp4"): resA,
		filepath.Join(dir, "b.mp4"): resB,
	}}}
	m := newJoinWizard(cfg, dir)

	m.Update(keyMsg("down"))
	m.Update(keyMsg(" "))
	m.Update(keyMsg("down"))
	m.Update(keyMsg(" "))
	m.Update(keyMsg("enter"))           // -> output
	_, cmd := m.Update(keyMsg("enter")) // -> probing
	var probe joinProbeMsg
	for _, msg := range msgsOf(cmd) {
		if p, ok := msg.(joinProbeMsg); ok {
			probe = p
		}
	}
	if probe.err != nil {
		t.Fatalf("probe failed: %v", probe.err)
	}
	m.Update(probe)
	if m.step != "confirm" {
		t.Fatalf("expected confirm step, got %q", m.step)
	}
	if len(m.fps) != 2 {
		t.Fatalf("expected two distinct frame rates, got %#v", m.fps)
	}
	if !strings.Contains(m.confirmView(), "different frame rates") {
		t.Fatalf("confirm view should warn about mixed frame rates:\n%s", m.confirmView())
	}
}

func TestJoinWizard_NoFpsNoteForUniformFrameRates(t *testing.T) {
	m := newJoinWizard(Config{}, t.TempDir())
	m.fps = []string{"30000/1001"}
	if m.fpsNote() != "" {
		t.Fatalf("uniform frame rates must not produce a note, got %q", m.fpsNote())
	}
}

// --- list size must survive navigation (rebuilt lists) ---

func TestFilePicker_NavigationPreservesListSize(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	m := newFilePicker(Config{}, root)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	w, h := m.list.Width(), m.list.Height()

	m.Update(keyMsg("down"))
	m.Update(keyMsg("enter")) // navigate into sub (rebuilds the list)
	if m.dir != sub {
		t.Fatalf("dir = %q, want %q", m.dir, sub)
	}
	if m.list.Width() != w || m.list.Height() != h {
		t.Fatalf("navigation must preserve list size: %dx%d, want %dx%d", m.list.Width(), m.list.Height(), w, h)
	}
}

func TestJoinWizard_NavigationPreservesListSize(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	m := newJoinWizard(Config{}, root)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	w, h := m.list.Width(), m.list.Height()

	m.Update(keyMsg("down"))
	m.Update(keyMsg("enter"))
	if m.list.Width() != w || m.list.Height() != h {
		t.Fatalf("navigation must preserve list size: %dx%d, want %dx%d", m.list.Width(), m.list.Height(), w, h)
	}
}

// --- M13: timestamps beyond EOF are rejected before ffmpeg runs ---

func TestTrimWizard_StartBeyondDurationRejected(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "clip.mp4")
	cfg := Config{Prober: &fakeProber{results: map[string]*ffprobe.ProbeResult{
		in: videoWithAudio("10"),
	}}}
	m := newTrimWizard(cfg, in)
	m.start.SetValue("12")
	m.end.SetValue("15")

	m.Update(keyMsg("enter")) // -> snapping
	m.Update(trimKeyframesMsg{keyframes: []float64{0, 2}, dur: 10})

	if m.step != "form" {
		t.Fatalf("expected to return to the form, got %q", m.step)
	}
	if m.err == "" {
		t.Fatal("a start at/after EOF must be rejected")
	}
}

func TestTrimWizard_EndBeyondDurationIsAllowed(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "clip.mp4")
	cfg := Config{Prober: &fakeProber{results: map[string]*ffprobe.ProbeResult{
		in: videoWithAudio("10"),
	}}}
	m := newTrimWizard(cfg, in)
	m.start.SetValue("0")
	m.end.SetValue("99") // beyond EOF: ffmpeg simply cuts at the end

	m.Update(keyMsg("enter"))
	_, cmd := m.Update(trimKeyframesMsg{keyframes: []float64{0, 2}, dur: 10})
	if !hasMsg[pushMsg](msgsOf(cmd)) {
		t.Fatal("an end beyond EOF must still run (cut to the end)")
	}
}

func TestScreenshotWizard_TimestampBeyondDurationRejected(t *testing.T) {
	m := newScreenshotWizard(Config{}, "/tmp/clip.mp4")
	m.Update(keyMsg("enter")) // png -> timestamp
	m.timestamp.SetValue("60")
	m.Update(keyMsg("enter")) // -> probing
	m.Update(screenshotDurMsg{dur: 10})

	if m.step != "timestamp" {
		t.Fatalf("expected to return to the timestamp step, got %q", m.step)
	}
	if m.err == "" {
		t.Fatal("a timestamp at/after EOF must be rejected")
	}
}

// --- H8: batch runs only start after the pre-flight confirmation ---

func TestBatchRun_PreFlightGatesTheRun(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "video.mp4")
	cfg := Config{Prober: &fakeProber{results: map[string]*ffprobe.ProbeResult{
		filepath.Join(dir, "video.mp4"): videoWithAudio("10"),
	}}, Runner: fakeRunner{}}

	m := newBatchRun(cfg, dir, "mp4", ffx.QualityMedium)
	if m.started || m.running {
		t.Fatal("construction must not start the run")
	}

	// esc before confirming pops back without starting.
	_, cmd := m.Update(keyMsg("esc"))
	if m.started {
		t.Fatal("esc on the pre-flight must not start the run")
	}
	if !hasMsg[popMsg](msgsOf(cmd)) {
		t.Fatal("esc on the pre-flight should pop the screen")
	}

	m.Update(keyMsg("enter"))
	if !m.started || !m.running {
		t.Fatal("enter must start the run")
	}
}

func TestBatchRun_PreFlightShowsOverwrites(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "video.mp4")
	writeFile(t, dir, "video_batch.mp4") // stale output from an earlier run
	cfg := Config{Prober: &fakeProber{}, Runner: fakeRunner{}}

	m := newBatchRun(cfg, dir, "mp4", ffx.QualityMedium)
	if m.overwrites != 1 {
		t.Fatalf("overwrites = %d, want 1", m.overwrites)
	}
	if !strings.Contains(m.View(), "already exist and will be overwritten") {
		t.Fatal("the pre-flight view must disclose overwrites before starting")
	}
}

// --- H6: the tool's own outputs are never re-converted ---

func TestBatchRun_SkipsOwnBatchOutputs(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "video.mp4")
	writeFile(t, dir, "video_batch.mp4") // output of a previous run
	cfg := Config{Prober: &fakeProber{results: map[string]*ffprobe.ProbeResult{
		filepath.Join(dir, "video.mp4"): videoWithAudio("10"),
	}}, Runner: fakeRunner{}}

	m := newBatchRun(cfg, dir, "mp4", ffx.QualityMedium)
	st, ok := m.runBatchCmd()().(batchStatusMsg)
	if !ok {
		t.Fatal("unexpected msg")
	}
	if st.ok != 1 || st.skipped != 1 || st.fail != 0 {
		t.Fatalf("ok=%d skipped=%d fail=%d", st.ok, st.skipped, st.fail)
	}
	if !strings.Contains(st.last, "previous batch output") {
		t.Fatalf("skip reason should mention the previous output, got %q", st.last)
	}
}

func TestIsOwnBatchOutput(t *testing.T) {
	cases := []struct {
		name, format string
		want         bool
	}{
		{"video_batch.mp4", "mp4", true},
		{"VIDEO_BATCH.MP4", "mp4", true},
		{"video_batch.mkv", "mp4", false},
		{"video.mp4", "mp4", false},
		{"batch.mp4", "mp4", false}, // stem "batch" does not end in _batch... it does not start with one
		{"my_batch_batch.mp4", "mp4", true},
	}
	for _, c := range cases {
		if got := isOwnBatchOutput(c.name, c.format); got != c.want {
			t.Errorf("isOwnBatchOutput(%q, %q) = %v, want %v", c.name, c.format, got, c.want)
		}
	}
}

// --- M19: the finished run shows a tail of ffmpeg stderr ---

func TestBatchRun_TailRenderedAfterCompletion(t *testing.T) {
	m := newBatchRun(Config{}, t.TempDir(), "mp4", ffx.QualityMedium)
	m.started = true
	m.Update(batchStatusMsg{ok: 1, tail: []string{"line one", "line two"}})
	if !strings.Contains(m.View(), "line two") || !strings.Contains(m.View(), "ffmpeg output (tail)") {
		t.Fatal("the summary view should include the stderr tail")
	}
}

// --- L-fix: the run view must not lie before the first file starts or
// when the run finished with failures ---

func TestBatchRun_PreStartViewShowsNoFileLine(t *testing.T) {
	m := newBatchRun(Config{}, t.TempDir(), "mp4", ffx.QualityMedium)
	if strings.Contains(m.View(), "File 0/") {
		t.Fatal("the pre-flight view must not show a file line")
	}

	m.started = true
	m.running = true
	m.totalFiles = 5
	if strings.Contains(m.View(), "File 0/") {
		t.Fatal("a run waiting for its first file must not show \"File 0/\"")
	}

	m.curIdx = 1
	if !strings.Contains(m.View(), "File 1/5") {
		t.Fatal("a started file must be shown with its 1-based position")
	}
}

func TestBatchRun_DoneDistinguishesFailures(t *testing.T) {
	m := newBatchRun(Config{}, t.TempDir(), "mp4", ffx.QualityMedium)
	m.started = true
	m.Update(batchStatusMsg{ok: 2, fail: 3})
	if !strings.Contains(m.View(), "Done with 3 failure(s)") {
		t.Fatalf("a run with failures must say so, got:\n%s", m.View())
	}

	m2 := newBatchRun(Config{}, t.TempDir(), "mp4", ffx.QualityMedium)
	m2.started = true
	m2.Update(batchStatusMsg{ok: 2})
	if strings.Contains(m2.View(), "failure") {
		t.Fatalf("a clean run must not report failures, got:\n%s", m2.View())
	}
	if !strings.Contains(m2.View(), "Done.") {
		t.Fatal("a clean run must report Done.")
	}
}

// --- M14: embedded cover art must not appear as a video track ---

func TestTracksWizard_CoverArtHiddenFromTrackList(t *testing.T) {
	res := videoWithAudio("10")
	res.Streams = append(res.Streams, ffprobe.Stream{
		Index: 2, CodecType: "video", CodecName: "mjpeg",
		Disposition: map[string]any{"attached_pic": float64(1)},
	})
	tvs := tracksFromProbe(res)
	for _, tv := range tvs {
		if tv.Track.Codec == "mjpeg" {
			t.Fatal("attached cover art must not be listed as a video track")
		}
	}
	if len(tvs) != 2 {
		t.Fatalf("want video+audio only, got %d tracks", len(tvs))
	}
}

// --- m18: focus ring stepping ---

func TestFocusStep(t *testing.T) {
	if got := focusStep(0, 4, 1); got != 1 {
		t.Fatalf("focusStep(0,4,1) = %d", got)
	}
	if got := focusStep(0, 4, -1); got != 3 {
		t.Fatalf("focusStep(0,4,-1) = %d, want wrap to 3", got)
	}
	if got := focusStep(3, 4, 1); got != 0 {
		t.Fatalf("focusStep(3,4,1) = %d, want wrap to 0", got)
	}
	if got := focusStep(2, 3, -1); got != 1 {
		t.Fatalf("focusStep(2,3,-1) = %d", got)
	}
}

// --- L-fix: esc walks exactly one step back through the tracks flow ---

func tracksWizardForTest() *tracksWizard {
	m := newTracksWizard(Config{}, "/tmp/x.mp4")
	m.loading = false
	m.tracks = []trackView{
		{Track: ffx.Track{Index: 0, Type: ffx.TrackVideo, Codec: "h264"}, Width: 640, Height: 360},
		{Track: ffx.Track{Index: 1, Type: ffx.TrackAudio, Codec: "aac"}, SampleRate: "48000", Channels: 2},
	}
	m.actions[0] = ffx.TrackActionInfo{Action: ffx.ActionKeep}
	m.actions[1] = ffx.TrackActionInfo{Action: ffx.ActionKeep}
	return m
}

func TestTracksWizard_EscStepsBackOneLevel(t *testing.T) {
	m := tracksWizardForTest()

	m.Update(keyMsg("enter")) // tracks -> output
	m.Update(keyMsg("enter")) // output -> confirm
	if m.step != "confirm" {
		t.Fatalf("expected the confirm step, got %q", m.step)
	}

	m.Update(keyMsg("esc"))
	if m.step != "output" {
		t.Fatalf("esc from confirm must return to the output step, got %q", m.step)
	}
	if !m.out.Focused() {
		t.Fatal("the output input must be focused again after esc from confirm")
	}

	m.Update(keyMsg("esc"))
	if m.step != "tracks" {
		t.Fatalf("esc from output must return to the tracks step, got %q", m.step)
	}

	m.Update(keyMsg("c")) // tracks -> codec
	if m.step != "codec" {
		t.Fatalf("expected the codec step, got %q", m.step)
	}
	m.Update(keyMsg("esc"))
	if m.step != "tracks" {
		t.Fatalf("esc from codec must return to the tracks step, got %q", m.step)
	}
}

// With more tracks than fit the viewport, moving the cursor to the last
// track must scroll it into view.
func TestTracksWizard_ViewFollowsCursor(t *testing.T) {
	m := tracksWizardForTest()
	for i := 2; i < 30; i++ {
		m.tracks = append(m.tracks, trackView{Track: ffx.Track{Index: i, Type: ffx.TrackAudio, Codec: "aac"}, SampleRate: "48000", Channels: 2})
		m.actions[i] = ffx.TrackActionInfo{Action: ffx.ActionKeep}
	}

	m.Update(tea.WindowSizeMsg{Width: 100, Height: 20})
	for i := 0; i < 29; i++ {
		m.Update(keyMsg("down"))
	}
	if m.cursor != 29 {
		t.Fatalf("cursor = %d, want 29", m.cursor)
	}

	view := m.View()
	last := trackLine(29, m.tracks[29])
	if !strings.Contains(view, last) {
		t.Fatalf("the view must scroll the cursor's line into view, missing %q:\n%s", last, view)
	}
}

// --- L-fix: small-terminal dimensions are clamped ---

func TestDimClampsAtOne(t *testing.T) {
	if got := dim(100, 9); got != 91 {
		t.Fatalf("dim(100,9) = %d", got)
	}
	if got := dim(2, 9); got != 1 {
		t.Fatalf("dim(2,9) = %d, want the 1 floor", got)
	}
	if got := dim(0, 4); got != 1 {
		t.Fatalf("dim(0,4) = %d, want the 1 floor", got)
	}
}

// --- L-fix: join frame rates dedupe numerically, not by spelling ---

func TestParseFps(t *testing.T) {
	if f, ok := parseFps("30000/1001"); !ok || math.Abs(f-29.97002997) > 1e-6 {
		t.Fatalf("parseFps(30000/1001) = %v %v", f, ok)
	}
	if f, ok := parseFps("30/1"); !ok || f != 30 {
		t.Fatalf("parseFps(30/1) = %v %v, want 30 true", f, ok)
	}
	if f, ok := parseFps("29.97"); !ok || f != 29.97 {
		t.Fatalf("parseFps(29.97) = %v %v", f, ok)
	}
	if _, ok := parseFps("abc"); ok {
		t.Fatal("unparseable rate must report !ok")
	}
	if _, ok := parseFps("0/0"); ok {
		t.Fatal("a zero denominator must report !ok")
	}
}

func TestFpsDistinct(t *testing.T) {
	if !fpsDistinct(nil, "24/1") {
		t.Fatal("an empty list is always distinct")
	}
	if fpsDistinct([]string{"24/1"}, "24000/1000") {
		t.Fatal("identical rates in different spellings must dedupe")
	}
	if fpsDistinct([]string{"30000/1001"}, "30000/1001") {
		t.Fatal("an identical rate must dedupe")
	}
	if !fpsDistinct([]string{"24/1"}, "25/1") {
		t.Fatal("different rates must stay distinct")
	}
	if !fpsDistinct([]string{"24/1"}, "junk") {
		t.Fatal("an unparseable rate must stay visible rather than be hidden")
	}
	if fpsDistinct([]string{"junk"}, "junk") {
		t.Fatal("identical unparseable rates must dedupe")
	}
}

// --- L-fix: a nil Prober degrades gracefully instead of panicking ---

func TestTracksWizard_NilProberSurfacesError(t *testing.T) {
	m := newTracksWizard(Config{}, "x.mp4")
	var probe tracksDoneMsg
	for _, msg := range msgsOf(m.Init()) {
		if p, ok := msg.(tracksDoneMsg); ok {
			probe = p
		}
	}
	if probe.err == nil {
		t.Fatal("a nil prober must produce a probe error, not panic")
	}
	m.Update(probe)
	if m.loading || m.err == "" {
		t.Fatalf("expected the error to be surfaced, loading=%v err=%q", m.loading, m.err)
	}
}

func TestInspectScreen_NilProberSurfacesError(t *testing.T) {
	m := newInspectScreen(Config{}, "x.mp4")
	var probe inspectDoneMsg
	for _, msg := range msgsOf(m.Init()) {
		if p, ok := msg.(inspectDoneMsg); ok {
			probe = p
		}
	}
	if probe.err == nil {
		t.Fatal("a nil prober must produce a probe error, not panic")
	}
	m.Update(probe)
	if !strings.Contains(m.View(), probe.err.Error()) {
		t.Fatalf("the view must show the probe error, got:\n%s", m.View())
	}
}

func TestExtractAudio_NilProberSurfacesError(t *testing.T) {
	m := newExtractAudioWizard(Config{}, "x.mp4")
	var probe audioCheckDoneMsg
	for _, msg := range msgsOf(m.Init()) {
		if p, ok := msg.(audioCheckDoneMsg); ok {
			probe = p
		}
	}
	if probe.err == nil {
		t.Fatal("a nil prober must produce a probe error, not panic")
	}
	m.Update(probe)
	if m.loading || m.err == "" {
		t.Fatalf("expected the error to be surfaced, loading=%v err=%q", m.loading, m.err)
	}
}

func TestJoinWizard_NilProberSurfacesError(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.mp4")
	writeFile(t, dir, "b.mp4")
	m := newJoinWizard(Config{}, dir)

	m.Update(keyMsg("down"))
	m.Update(keyMsg(" "))
	m.Update(keyMsg("down"))
	m.Update(keyMsg(" "))
	m.Update(keyMsg("enter"))           // -> output
	_, cmd := m.Update(keyMsg("enter")) // -> probing
	var probe joinProbeMsg
	for _, msg := range msgsOf(cmd) {
		if p, ok := msg.(joinProbeMsg); ok {
			probe = p
		}
	}
	if probe.err == nil {
		t.Fatal("a nil prober must produce a probe error, not panic")
	}
	m.Update(probe)
	if m.step != "select" || m.err == "" {
		t.Fatalf("expected the error on the select step, got step=%q err=%q", m.step, m.err)
	}
}

func TestBatchRun_NilProberFailsFilesInsteadOfPanicking(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "video.mp4")
	m := newBatchRun(Config{Runner: fakeRunner{}}, dir, "mp4", ffx.QualityMedium)
	st, ok := m.runBatchCmd()().(batchStatusMsg)
	if !ok {
		t.Fatal("unexpected msg")
	}
	if st.fail != 1 || st.ok != 0 {
		t.Fatalf("fail=%d ok=%d, want the file to fail gracefully", st.fail, st.ok)
	}
}

// --- bulk actions in the tracks wizard (Shift+K / Shift+R / Shift+C) ---

func TestTrackLineAudioTitle(t *testing.T) {
	base := trackView{
		Track:      ffx.Track{Index: 1, Type: ffx.TrackAudio, Codec: "aac"},
		SampleRate: "48000",
		Channels:   2,
		Language:   "eng",
	}

	if line := trackLine(1, base); !strings.HasSuffix(line, "eng") {
		t.Fatalf("a track without a title must end at the language, got %q", line)
	}

	titled := base
	titled.Title = "Commentary"
	if line := trackLine(1, titled); !strings.Contains(line, "eng  Commentary") {
		t.Fatalf("audio lines must carry the title like subs do, got %q", line)
	}

	long := base
	long.Title = strings.Repeat("Very Long Commentary Track Name ", 3)
	line := trackLine(1, long)
	if strings.Contains(line, "\n") {
		t.Fatalf("a long title must never wrap the line, got %q", line)
	}
	if !strings.Contains(line, "…") {
		t.Fatalf("an over-budget title must be truncated with an ellipsis, got %q", line)
	}
	// The title is the audio line's last segment; measure it directly.
	sep := "eng  "
	rest := line[strings.LastIndex(line, sep)+len(sep):]
	if len([]rune(rest)) > trackTitleBudget {
		t.Fatalf("truncated title segment is %d runes, want at most %d: %q", len([]rune(rest)), trackTitleBudget, rest)
	}
}

func TestTracksWizard_BulkKeysSetAllActions(t *testing.T) {
	m := tracksWizardForTest()
	m.tracks = append(m.tracks,
		trackView{Track: ffx.Track{Index: 2, Type: ffx.TrackSubtitle, Codec: "srt"}, Language: "eng"},
	)
	m.actions[2] = ffx.TrackActionInfo{Action: ffx.ActionKeep}

	m.Update(keyMsg("R"))
	for i := range m.tracks {
		if m.actions[i].Action != ffx.ActionRemove {
			t.Fatalf("Shift+R must remove every track, track %d = %s", i, m.actions[i].Action)
		}
	}

	m.Update(keyMsg("K"))
	for i := range m.tracks {
		if m.actions[i].Action != ffx.ActionKeep {
			t.Fatalf("Shift+K must keep every track, track %d = %s", i, m.actions[i].Action)
		}
	}
}

func TestTracksWizard_BulkRemoveAllCannotConfirm(t *testing.T) {
	m := tracksWizardForTest()
	m.tracks = append(m.tracks,
		trackView{Track: ffx.Track{Index: 2, Type: ffx.TrackSubtitle, Codec: "srt"}, Language: "eng"},
	)
	m.actions[2] = ffx.TrackActionInfo{Action: ffx.ActionKeep}

	m.Update(keyMsg("R"))
	m.Update(keyMsg("enter")) // tracks -> output
	m.Update(keyMsg("enter")) // output -> confirm
	if m.step != "confirm" {
		t.Fatalf("expected the confirm step, got %q", m.step)
	}

	_, cmd := m.Update(keyMsg("enter"))
	if cmd != nil {
		t.Fatal("an all-removed model must not push the exec screen")
	}
	if m.step != "tracks" {
		t.Fatalf("the all-removed error must fall back to the tracks step, got %q", m.step)
	}
	if m.err != "all tracks are removed; nothing to output" {
		t.Fatalf("err = %q, want the all-removed message", m.err)
	}
}

func TestTracksWizard_BulkConvertByType(t *testing.T) {
	m := tracksWizardForTest()
	m.tracks = append(m.tracks,
		trackView{Track: ffx.Track{Index: 2, Type: ffx.TrackAudio, Codec: "dts"}, SampleRate: "48000", Channels: 6, Language: "deu"},
		trackView{Track: ffx.Track{Index: 3, Type: ffx.TrackSubtitle, Codec: "srt"}, Language: "eng"},
	)
	m.actions[2] = ffx.TrackActionInfo{Action: ffx.ActionKeep}
	m.actions[3] = ffx.TrackActionInfo{Action: ffx.ActionKeep}
	m.actions[0] = ffx.TrackActionInfo{Action: ffx.ActionRemove} // must stay untouched

	m.Update(keyMsg("down")) // cursor on the first audio track
	m.Update(keyMsg("C"))
	if m.step != "codec" || !m.codecBulk {
		t.Fatalf("Shift+C must open the codec chooser in bulk mode, step=%q bulk=%v", m.step, m.codecBulk)
	}
	if m.codecList.Title != "Select codec for all audio tracks" {
		t.Fatalf("chooser title = %q, want the bulk audio title", m.codecList.Title)
	}
	if m.codecBulkType != ffx.TrackAudio {
		t.Fatalf("bulk type = %q, want audio (captured at open, not from the cursor)", m.codecBulkType)
	}

	m.Update(keyMsg("enter")) // first audio option is aac
	if m.step != "tracks" || m.codecBulk {
		t.Fatalf("applying must leave the codec step and reset bulk, step=%q bulk=%v", m.step, m.codecBulk)
	}
	for i := 1; i <= 2; i++ {
		if m.actions[i] != (ffx.TrackActionInfo{Action: ffx.ActionConvert, Codec: "aac"}) {
			t.Fatalf("audio track %d = %+v, want convert:aac", i, m.actions[i])
		}
	}
	if m.actions[0].Action != ffx.ActionRemove {
		t.Fatalf("video action must be untouched, got %s", m.actions[0].Action)
	}
	if m.actions[3].Action != ffx.ActionKeep {
		t.Fatalf("subtitle action must be untouched, got %s", m.actions[3].Action)
	}
}

func TestTracksWizard_BulkConvertEscCancels(t *testing.T) {
	m := tracksWizardForTest()
	before := m.actions[1]

	m.Update(keyMsg("down"))
	m.Update(keyMsg("C"))
	if m.step != "codec" {
		t.Fatalf("expected the codec step, got %q", m.step)
	}
	m.Update(keyMsg("esc"))
	if m.step != "tracks" || m.codecBulk {
		t.Fatalf("esc must cancel the bulk chooser, step=%q bulk=%v", m.step, m.codecBulk)
	}
	if m.actions[1] != before {
		t.Fatalf("esc must not change any action, track 1 = %+v, want %+v", m.actions[1], before)
	}
}

func TestTracksWizard_SingleTrackKeysStayOnCursor(t *testing.T) {
	m := tracksWizardForTest()
	m.tracks = append(m.tracks,
		trackView{Track: ffx.Track{Index: 2, Type: ffx.TrackSubtitle, Codec: "srt"}, Language: "eng"},
	)
	m.actions[2] = ffx.TrackActionInfo{Action: ffx.ActionKeep}

	m.Update(keyMsg("r"))
	if m.actions[0].Action != ffx.ActionRemove || m.actions[1].Action != ffx.ActionKeep || m.actions[2].Action != ffx.ActionKeep {
		t.Fatalf("r must only touch the cursor track: %v %v %v", m.actions[0], m.actions[1], m.actions[2])
	}

	m.Update(keyMsg("k"))
	if m.actions[0].Action != ffx.ActionKeep {
		t.Fatalf("k must only touch the cursor track, track 0 = %s", m.actions[0].Action)
	}

	m.Update(keyMsg("down"))
	m.Update(keyMsg("c"))
	if m.step != "codec" || m.codecBulk {
		t.Fatalf("c must open the single-track chooser, step=%q bulk=%v", m.step, m.codecBulk)
	}
	if m.codecList.Title != "Select codec (audio)" {
		t.Fatalf("single-track chooser title = %q, want the unchanged title", m.codecList.Title)
	}
	m.Update(keyMsg("enter")) // first audio option is aac
	if m.actions[0] != (ffx.TrackActionInfo{Action: ffx.ActionKeep}) || m.actions[2].Action != ffx.ActionKeep {
		t.Fatal("a single-track convert must not leak to other tracks")
	}
	if m.actions[1] != (ffx.TrackActionInfo{Action: ffx.ActionConvert, Codec: "aac"}) {
		t.Fatalf("cursor track = %+v, want convert:aac", m.actions[1])
	}
}

// A title longer than the viewport width must not wrap its track line:
// the list scrolls exactly one line per track.
func TestTracksWizard_LongTitleStaysOneLine(t *testing.T) {
	m := tracksWizardForTest()
	longTitle := strings.Repeat("Long Commentary Track Name ", 4)
	m.tracks[1].Title = longTitle
	m.tracks = append(m.tracks,
		trackView{Track: ffx.Track{Index: 2, Type: ffx.TrackSubtitle, Codec: "srt"}, Language: "eng", Title: longTitle},
	)
	m.actions[2] = ffx.TrackActionInfo{Action: ffx.ActionKeep}

	m.Update(tea.WindowSizeMsg{Width: 100, Height: 20})
	view := m.View()

	lines := 0
	for _, ln := range strings.Split(view, "\n") {
		if strings.Contains(ln, "idx=") {
			lines++
		}
	}
	if lines != len(m.tracks) {
		t.Fatalf("the view renders %d track lines for %d tracks — a title wrapped:\n%s", lines, len(m.tracks), view)
	}
	for i, tv := range m.tracks {
		if !strings.Contains(view, trackLine(i, tv)) {
			t.Fatalf("track %d must render on a single line, missing %q:\n%s", i, trackLine(i, tv), view)
		}
	}
	if strings.Contains(view, longTitle) {
		t.Fatal("an over-budget title must be truncated in the view")
	}
}
