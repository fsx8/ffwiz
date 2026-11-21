package app

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	ffx "github.com/fsx8/ffwiz/internal/ffmpeg"
	"github.com/fsx8/ffwiz/internal/ffprobe"
)

// tracksWizardLoadedForDir builds a tracks wizard over dir/movie.mp4 with
// one probed video track, ready for the tracks step.
func tracksWizardLoadedForDir(dir string) *tracksWizard {
	m := newTracksWizard(Config{}, filepath.Join(dir, "movie.mp4"))
	m.loading = false
	m.tracks = []trackView{{Track: ffx.Track{Index: 0, Type: ffx.TrackVideo, Codec: "h264"}, Width: 640, Height: 360}}
	m.actions[0] = ffx.TrackActionInfo{Action: ffx.ActionKeep}
	return m
}

func addFileNames(m *tracksWizard) []string {
	var names []string
	for _, it := range m.addList.Items() {
		switch v := it.(type) {
		case dirItem:
			names = append(names, v.name)
		case addFileItem:
			names = append(names, v.name)
		}
	}
	return names
}

// The add browser lists directories plus subtitle/audio sources only: a
// .txt and the .mp4 media itself must not be selectable add sources.
func TestTracksWizard_AddBrowserListsOnlySubtitleAndAudioFiles(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "movie.mp4")
	writeFile(t, dir, "notes.txt")
	writeFile(t, dir, "subs.srt")
	writeFile(t, dir, "song.mp3")
	writeFile(t, dir, "extra.ass")
	if err := os.Mkdir(filepath.Join(dir, "sideloads"), 0o755); err != nil {
		t.Fatal(err)
	}

	m := tracksWizardLoadedForDir(dir)
	m.Update(keyMsg("a"))
	if m.step != "add" {
		t.Fatalf("a must open the add browser, got step %q", m.step)
	}

	names := addFileNames(m)
	for _, want := range []string{"..", "sideloads", "subs.srt", "song.mp3", "extra.ass"} {
		if !slices.Contains(names, want) {
			t.Fatalf("add browser must list %q, items: %#v", want, names)
		}
	}
	for _, banned := range []string{"movie.mp4", "notes.txt"} {
		if slices.Contains(names, banned) {
			t.Fatalf("add browser must not list %q, items: %#v", banned, names)
		}
	}
}

func probeMsgOf(t *testing.T, cmd tea.Cmd) addProbeMsg {
	t.Helper()
	for _, msg := range msgsOf(cmd) {
		if p, ok := msg.(addProbeMsg); ok {
			return p
		}
	}
	t.Fatal("no addProbeMsg produced")
	return addProbeMsg{}
}

// Probing a picked file appends its subtitle/audio streams as NEW entries
// with SourceFile/SourceIndex set, keeps their action, lands the cursor on
// the first added entry, and makes the preview a two-input command.
func TestTracksWizard_AddProbeAppendsTracks(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "movie.mp4")
	writeFile(t, dir, "subs.srt")
	writeFile(t, dir, "song.flac")
	srt := filepath.Join(dir, "subs.srt")
	flac := filepath.Join(dir, "song.flac")
	cfg := Config{Prober: &fakeProber{results: map[string]*ffprobe.ProbeResult{
		srt:  {Streams: []ffprobe.Stream{{Index: 0, CodecType: "subtitle", CodecName: "subrip", Tags: map[string]string{"language": "eng"}}}},
		flac: {Streams: []ffprobe.Stream{{Index: 0, CodecType: "audio", CodecName: "flac", SampleRate: "48000", Channels: 2}}},
	}}}
	m := newTracksWizard(cfg, filepath.Join(dir, "movie.mp4"))
	m.loading = false
	m.tracks = []trackView{
		{Track: ffx.Track{Index: 0, Type: ffx.TrackVideo, Codec: "h264"}},
		{Track: ffx.Track{Index: 1, Type: ffx.TrackAudio, Codec: "aac"}},
	}
	m.actions[0] = ffx.TrackActionInfo{Action: ffx.ActionKeep}
	m.actions[1] = ffx.TrackActionInfo{Action: ffx.ActionKeep}

	m.Update(keyMsg("a"))
	m.Update(keyMsg("down")) // song.flac
	m.Update(keyMsg("down")) // subs.srt (natural order)
	_, cmd := m.Update(keyMsg("enter"))
	if !m.probing {
		t.Fatal("enter on a side file must start the probe")
	}
	probe := probeMsgOf(t, cmd)
	if probe.err != nil {
		t.Fatalf("probe failed: %v", probe.err)
	}
	m.Update(probe)
	if m.probing {
		t.Fatal("a completed probe must clear the probing flag")
	}
	if len(m.tracks) != 3 {
		t.Fatalf("the added subtitle must be appended, got %d tracks", len(m.tracks))
	}
	added := m.tracks[2]
	if added.Track.SourceFile != srt || added.Track.SourceIndex != 0 ||
		added.Track.Type != ffx.TrackSubtitle || added.Track.Codec != "subrip" {
		t.Fatalf("added track = %+v, want subs.srt stream 0", added.Track)
	}
	if added.Language != "eng" {
		t.Fatalf("added track must carry its probed language, got %q", added.Language)
	}
	if m.actions[2].Action != ffx.ActionKeep {
		t.Fatalf("added tracks must default to keep, got %s", m.actions[2].Action)
	}
	if m.cursor != 2 {
		t.Fatalf("cursor = %d, want the first added entry", m.cursor)
	}

	preview := m.previewCommand()
	if got := strings.Count(preview, " -i "); got != 2 || !strings.HasPrefix(preview, "ffmpeg -i ") {
		t.Fatalf("preview must show two inputs, got: %s", preview)
	}
	if !strings.Contains(preview, srt) {
		t.Fatalf("preview must map the sidecar input, got: %s", preview)
	}

	// A second add from the same browser: the flac becomes a fourth line
	// and its input slot is distinct.
	m.Update(keyMsg("a"))
	if m.step != "add" {
		t.Fatalf("expected to re-enter the add browser, got %q", m.step)
	}
	m.Update(keyMsg("down")) // song.flac (".." is first)
	_, cmd = m.Update(keyMsg("enter"))
	m.Update(probeMsgOf(t, cmd))
	if len(m.tracks) != 4 {
		t.Fatalf("the added audio must be appended, got %d tracks", len(m.tracks))
	}
	if got := m.tracks[3].Track; got.SourceFile != flac || got.SourceIndex != 0 || got.Type != ffx.TrackAudio {
		t.Fatalf("second added track = %+v, want song.flac stream 0", got)
	}
	if m.cursor != 3 {
		t.Fatalf("cursor = %d, want the second added entry", m.cursor)
	}
}

// A side file without addable streams must surface an error and add
// nothing.
func TestTracksWizard_AddProbeWithoutAddableStreamsSurfacesError(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "movie.mp4")
	writeFile(t, dir, "weird.vtt")
	path := filepath.Join(dir, "weird.vtt")
	cfg := Config{Prober: &fakeProber{results: map[string]*ffprobe.ProbeResult{
		path: {Streams: []ffprobe.Stream{{Index: 0, CodecType: "video", CodecName: "mjpeg"}}},
	}}}
	m := newTracksWizard(cfg, filepath.Join(dir, "movie.mp4"))
	m.loading = false
	m.tracks = []trackView{{Track: ffx.Track{Index: 0, Type: ffx.TrackVideo, Codec: "h264"}}}
	m.actions[0] = ffx.TrackActionInfo{Action: ffx.ActionKeep}

	m.Update(keyMsg("a"))
	m.Update(keyMsg("down")) // weird.vtt
	_, cmd := m.Update(keyMsg("enter"))
	m.Update(probeMsgOf(t, cmd))
	if !strings.Contains(m.err, "no subtitle or audio tracks") {
		t.Fatalf("expected a no-addable-streams error, got %q", m.err)
	}
	if len(m.tracks) != 1 {
		t.Fatalf("nothing may be added, got %d tracks", len(m.tracks))
	}
}

func TestTracksWizard_AddProbeErrorSurfacesError(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "movie.mp4")
	writeFile(t, dir, "subs.srt")
	path := filepath.Join(dir, "subs.srt")
	cfg := Config{Prober: &fakeProber{err: map[string]error{path: errNoTracks{}}}}
	m := newTracksWizard(cfg, filepath.Join(dir, "movie.mp4"))
	m.loading = false
	m.tracks = []trackView{{Track: ffx.Track{Index: 0, Type: ffx.TrackVideo, Codec: "h264"}}}
	m.actions[0] = ffx.TrackActionInfo{Action: ffx.ActionKeep}

	m.Update(keyMsg("a"))
	m.Update(keyMsg("down"))
	_, cmd := m.Update(keyMsg("enter"))
	m.Update(probeMsgOf(t, cmd))
	if m.err == "" {
		t.Fatal("a failed probe must surface its error in the browser")
	}
	if len(m.tracks) != 1 {
		t.Fatalf("nothing may be added on a failed probe, got %d tracks", len(m.tracks))
	}
}

// A probe result arriving after the user left the add browser is stale and
// must not append tracks behind their back.
func TestTracksWizard_StaleAddProbeAfterEscIgnored(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "movie.mp4")
	writeFile(t, dir, "subs.srt")
	srt := filepath.Join(dir, "subs.srt")
	cfg := Config{Prober: &fakeProber{results: map[string]*ffprobe.ProbeResult{
		srt: {Streams: []ffprobe.Stream{{Index: 0, CodecType: "subtitle", CodecName: "subrip"}}},
	}}}
	m := newTracksWizard(cfg, filepath.Join(dir, "movie.mp4"))
	m.loading = false
	m.tracks = []trackView{{Track: ffx.Track{Index: 0, Type: ffx.TrackVideo, Codec: "h264"}}}
	m.actions[0] = ffx.TrackActionInfo{Action: ffx.ActionKeep}

	m.Update(keyMsg("a"))
	m.Update(keyMsg("down"))
	_, _ = m.Update(keyMsg("enter")) // probing starts
	m.Update(keyMsg("esc"))          // user backs out mid-probe
	if m.step != "tracks" {
		t.Fatalf("esc must leave the browser, got %q", m.step)
	}
	m.Update(addProbeMsg{path: srt, res: &ffprobe.ProbeResult{
		Streams: []ffprobe.Stream{{Index: 0, CodecType: "subtitle", CodecName: "subrip"}},
	}})
	if len(m.tracks) != 1 {
		t.Fatalf("a stale probe result must not append tracks, got %d", len(m.tracks))
	}
}

// Two probes racing: a superseded probe's late result must not append its
// file while a newer probe is in flight — after Esc and a second pick, the
// probing flag alone cannot tell the two in-flight results apart.
func TestTracksWizard_SupersededAddProbeResultIgnored(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "movie.mp4")
	writeFile(t, dir, "a.srt")
	writeFile(t, dir, "b.srt")
	a, b := filepath.Join(dir, "a.srt"), filepath.Join(dir, "b.srt")
	sub := func() *ffprobe.ProbeResult {
		return &ffprobe.ProbeResult{Streams: []ffprobe.Stream{{Index: 0, CodecType: "subtitle", CodecName: "subrip"}}}
	}
	cfg := Config{Prober: &fakeProber{results: map[string]*ffprobe.ProbeResult{a: sub(), b: sub()}}}
	m := newTracksWizard(cfg, filepath.Join(dir, "movie.mp4"))
	m.loading = false
	m.tracks = []trackView{{Track: ffx.Track{Index: 0, Type: ffx.TrackVideo, Codec: "h264"}}}
	m.actions[0] = ffx.TrackActionInfo{Action: ffx.ActionKeep}

	m.Update(keyMsg("a"))
	m.Update(keyMsg("down"))             // a.srt
	_, cmdA := m.Update(keyMsg("enter")) // probe A starts
	msgA := probeMsgOf(t, cmdA)
	m.Update(keyMsg("esc")) // back out mid-probe; A still in flight

	m.Update(keyMsg("a")) // reopen and pick b.srt unresolved-A
	m.Update(keyMsg("down"))
	m.Update(keyMsg("down"))
	_, cmdB := m.Update(keyMsg("enter")) // probe B starts
	msgB := probeMsgOf(t, cmdB)

	m.Update(msgA) // A's late result arrives while B is probing
	if len(m.tracks) != 1 {
		t.Fatalf("a superseded probe result must be ignored, got %d tracks", len(m.tracks))
	}
	m.Update(msgB) // the probe the user actually asked for lands
	if len(m.tracks) != 2 || m.tracks[1].Track.SourceFile != b {
		t.Fatalf("the picked probe must append its file, tracks=%d tracks[1]=%+v", len(m.tracks), m.tracks[1].Track)
	}
}

// r on an imported track deletes it from the list and shifts the
// position-keyed actions; r on original tracks stays a remove action.
func TestTracksWizard_DeleteAddedTrackReindexes(t *testing.T) {
	m := tracksWizardForTest()
	m.tracks = append(m.tracks,
		trackView{Track: ffx.Track{SourceFile: "/tmp/a.srt", Index: 0, SourceIndex: 0, Type: ffx.TrackSubtitle, Codec: "subrip"}},
		trackView{Track: ffx.Track{SourceFile: "/tmp/b.mp3", Index: 0, SourceIndex: 0, Type: ffx.TrackAudio, Codec: "mp3"}},
	)
	m.actions[2] = ffx.TrackActionInfo{Action: ffx.ActionKeep}
	m.actions[3] = ffx.TrackActionInfo{Action: ffx.ActionKeep}

	m.Update(keyMsg("r"))
	if m.actions[0].Action != ffx.ActionRemove || len(m.tracks) != 4 {
		t.Fatalf("r on an original track must stay a remove action: action=%s tracks=%d", m.actions[0].Action, len(m.tracks))
	}
	m.actions[0] = ffx.TrackActionInfo{Action: ffx.ActionKeep}

	m.Update(keyMsg("down"))
	m.Update(keyMsg("down")) // cursor on a.srt
	m.Update(keyMsg("r"))
	if len(m.tracks) != 3 {
		t.Fatalf("the added track must be deleted, got %d tracks", len(m.tracks))
	}
	if m.tracks[2].Track.SourceFile != "/tmp/b.mp3" {
		t.Fatalf("the remaining added track shifted down, got %+v", m.tracks[2].Track)
	}
	if _, stale := m.actions[3]; stale {
		t.Fatal("a stale action entry beyond the list must be gone")
	}
	if m.actions[2].Action != ffx.ActionKeep || m.actions[0].Action != ffx.ActionKeep {
		t.Fatalf("actions must survive the shift: %+v", m.actions)
	}
	if m.cursor != 2 {
		t.Fatalf("cursor = %d, want clamped to the new last entry", m.cursor)
	}

	m.Update(keyMsg("r")) // delete the last added track too
	if len(m.tracks) != 2 || m.cursor != 1 {
		t.Fatalf("after deleting the last added track: tracks=%d cursor=%d, want 2/1", len(m.tracks), m.cursor)
	}
}

// An imported kept entry is labeled NEW while originals still read KEEP.
func TestTracksWizard_AddedEntryShowsNewMarker(t *testing.T) {
	m := tracksWizardForTest()
	m.tracks = append(m.tracks,
		trackView{Track: ffx.Track{SourceFile: "/tmp/a.srt", Index: 0, SourceIndex: 0, Type: ffx.TrackSubtitle, Codec: "subrip"}, Language: "eng"},
	)
	m.actions[2] = ffx.TrackActionInfo{Action: ffx.ActionKeep}
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 20})

	view := m.View()
	if !strings.Contains(view, "[NEW]") {
		t.Fatalf("a kept added track must be labeled NEW, view:\n%s", view)
	}
	if !strings.Contains(view, "[KEEP]") {
		t.Fatalf("original tracks must still read KEEP, view:\n%s", view)
	}
	if !strings.Contains(view, "a.srt") {
		t.Fatalf("the added line must name its source file, view:\n%s", view)
	}
}

// Long external file names must not wrap the added track's line: the
// viewport invariant is exactly one line per track.
func TestTracksWizard_LongExternalNameStaysOneLine(t *testing.T) {
	m := tracksWizardForTest()
	longName := strings.Repeat("a-very-long-external-subtitle-filename-", 3) + ".srt"
	longTitle := strings.Repeat("Very Long Commentary Track Name ", 3)
	m.tracks = append(m.tracks,
		trackView{Track: ffx.Track{SourceFile: "/movies/" + longName, Index: 0, SourceIndex: 0, Type: ffx.TrackSubtitle, Codec: "subrip"}, Language: "eng", Title: longTitle},
	)
	m.actions[2] = ffx.TrackActionInfo{Action: ffx.ActionKeep}

	line := trackLine(2, m.tracks[2])
	if strings.Contains(line, "\n") {
		t.Fatalf("a long external file name must never wrap the line, got %q", line)
	}
	if strings.Contains(line, longName) {
		t.Fatal("an over-budget file name must be truncated")
	}
	if !strings.Contains(line, "…") {
		t.Fatalf("truncation must be visible, got %q", line)
	}

	m.Update(tea.WindowSizeMsg{Width: 100, Height: 20})
	view := m.View()
	fileLines, idxLines := 0, 0
	for _, ln := range strings.Split(view, "\n") {
		if strings.Contains(ln, "file=") {
			fileLines++
		}
		if strings.Contains(ln, "idx=") {
			idxLines++
		}
	}
	if fileLines != 1 || idxLines != 2 {
		t.Fatalf("one line per track invariant broken: file lines=%d idx lines=%d\n%s", fileLines, idxLines, view)
	}
}

func TestTracksWizard_HelpAdvertisesAddTrack(t *testing.T) {
	m := tracksWizardForTest()
	if view := m.View(); !strings.Contains(view, "a add track") {
		t.Fatalf("the help line must advertise a add track, view:\n%s", view)
	}
}

// While the add browser's filter is being typed into, q and esc must reach
// the filter instead of quitting or leaving the step.
func TestTracksWizard_AddBrowserFilterSwallowsQAndEsc(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "subs.srt")
	writeFile(t, dir, "songs.srt")
	m := tracksWizardLoadedForDir(dir)

	m.Update(keyMsg("a"))
	m.Update(keyMsg("/"))
	if !filterActive(m.addList) {
		t.Fatal("expected filter mode after '/'")
	}
	_, cmd := m.Update(keyMsg("q"))
	if hasMsg[tea.QuitMsg](msgsOf(cmd)) {
		t.Fatal("q must type into the filter, not quit")
	}
	pumpFiltering(t, m, cmd)
	m.Update(keyMsg("esc"))
	if filterActive(m.addList) || m.step != "add" {
		t.Fatalf("esc during filtering must clear the filter and stay, state=%v step=%q", m.addList.FilterState(), m.step)
	}
	_, cmd = m.Update(keyMsg("esc"))
	if m.step != "tracks" {
		t.Fatalf("a second esc must return to the tracks step, got %q", m.step)
	}
	if hasMsg[popMsg](msgsOf(cmd)) {
		t.Fatal("esc from the add browser must not pop the wizard")
	}
}

func TestTracksWizard_AddBrowserDirectoryNavigation(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "movie.mp4")
	sub := filepath.Join(root, "sideloads")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, sub, "track.srt")
	m := tracksWizardLoadedForDir(root)

	m.Update(keyMsg("a"))
	m.Update(keyMsg("down")) // ".." -> sideloads
	m.Update(keyMsg("enter"))
	if m.addDir != sub {
		t.Fatalf("dir = %q, want %q", m.addDir, sub)
	}
	if !slices.Contains(addFileNames(m), "track.srt") {
		t.Fatalf("expected the subdirectory's side file, items: %#v", addFileNames(m))
	}

	m.Update(keyMsg("esc"))
	if m.step != "tracks" {
		t.Fatalf("esc must return to the tracks step, got %q", m.step)
	}
	m.Update(keyMsg("a"))
	if m.addDir != sub {
		t.Fatalf("reopening must remember the last browse dir, got %q", m.addDir)
	}
	m.Update(keyMsg("enter")) // cursor rests on ".."
	if m.addDir != root {
		t.Fatalf(".. must walk back up, dir = %q", m.addDir)
	}
}
