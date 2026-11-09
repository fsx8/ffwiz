# ffwiz Codebase Review

**Verification baseline:** `gofmt` clean, `go vet` clean, `go build` OK, unit tests pass, all 40+ integration tests pass against real ffmpeg 7.0.2 with the generated fixtures. The core is solid — findings below are the gaps around it.

---

## High

**H1. Esc during trim keyframe-snapping doesn't cancel — a cancelled trim still launches.** `internal/app/screen_trim.go:75-76` handles `trimKeyframesMsg` with no step guard. `Esc` at :80-83 returns to the form, but the probe goroutine (:161-174) keeps running (its `kfCtx` cancel is never stored), and when the message lands, `startTrim` (:180) unconditionally pushes the exec screen using stale `m.pending` — **ffmpeg starts writing the output file the user cancelled**. Re-submitting a new trim meanwhile can race so the _old_ cut wins. Fix: `if m.step != "snapping" { return m, nil }` in the msg case, plus store/call a cancel func on Esc (join already does this correctly — `screen_join.go:183-186`).

**H2. Silent multi-stream loss on trim, batch copy, and single-stream re-encodes.** Verified empirically on `hdr4k.mkv` (3 audio + 4 subs):

- Trim (`-c copy`, `trim.go:19`): output kept **1 audio + 1 subtitle** — the other 5 streams vanish. The metadata editor deliberately uses `-map 0` to prevent exactly this (`metadata.go:30`); trim/compress/resize/rotate/flip/crop/speed don't.
- Batch "same quality (copy)" (`batch.go:40`) mkv→mp4: all subtitles + 2 audio tracks silently dropped (exit 0, no warning).
- Speed/reverse (`effects.go:84,110`) use `[0:a]` — ffmpeg binds only the _first_ matching stream, so the extra tracks are silently re-encoded away.

For the "movie with several audio tracks" audience this is data loss without any warning. Suggest `-map 0` (+ `-c:s`/`-c:a` handling per container, or at least a probe-driven warning) for copy paths, and `0:a:0`-style explicit specifiers elsewhere.

## Medium

**M1. PGS/DVB bitmap subs → mov_text is a doomed conversion.** `containers.go:81-86` unconditionally returns `mov_text` for any subtitle into mp4. Converting bitmap subtitles (Blu-ray rips — a core audience) fails the whole command with ffmpeg's cryptic "Subtitle encoding currently only possible from text to text or bitmap to bitmap". Detect bitmap codecs (`hdmv_pgs_subtitle`, `dvb_sub`, `dvd_subtitle`, …) and drop them (like webm) instead of emitting a command that will fail.

**M2. Stale-probe guards missing on three wizard screens.** `screen_compress.go:169-180`, `screen_resize.go:165-179`, `screen_screenshot.go:126-135` fall through to the hidden list's `Update` during the "probing" step — arrow keys silently move an invisible cursor. Sibling screens (effects :176, transform :180, metadata :161, join :262) all early-return. Related: `screen_transform.go:141-144` leaves `m.probing` set after Esc (effects resets it).

**M3. Batch list error never clears.** `screen_batch.go:58-64` sets `m.listErr` on failure but the success path of the next `ListDir` never resets it — the red error sticks while navigating until you pick a format. File-picker and join both clear in the else branch.

**M4. README `.deb` URL is wrong.** `README.md:125-126` documents `ffwiz_<version>_linux_<arch>.deb`; `ConventionalFileName` in `.goreleaser.yaml` produces `ffwiz_<version>_amd64.deb`. The documented command 404s.

**M5. npm installer: no checksum verification + prerelease mismatch.** `npm/install.js:27` maps an npm prerelease (`0.2.0-beta.1`) to the base tag's assets; and the archive is installed without checking goreleaser's published `checksums.txt` (supply-chain hardening that's one fetch + sha256 away). Also `run.js:10` exits 1 silently when the postinstall binary is missing.

**M6. One-shot probes are never cancelled on quit.** Join's `q` during probing (`screen_join.go:165-169`) and ctrl+c anywhere (`root.go:44-53` forwards but probe-owning screens have no ctrl+c case) leave ffprobe (worst case: the `-skip_frame nokey` keyframe scan) running to completion because `execx` process groups aren't killed on exit. Fix: cancel probe contexts on q/ctrl+c, or derive all probes from an app-lifetime context cancelled at quit.

**M7. apt workflow hardening.** `apt.yml:54` interpolates the GPG secret straight into shell (`if [ -z "${{ secrets... }}" ]`) — pass via `env:` like line 66 already does; same for `${{ inputs.tag }}` (:40) and the npm.yml equivalent. `apt.yml:119-124` ships a byte-copy of the amd64 `Packages` as arm64 — works only because apt filters stanzas by `Architecture:`, and one "cleanup" away from silently emptying arm64. Generate per-arch with `dpkg-scanpackages --arch`.

## Low / polish

- **Help lies:** trim's help advertises "q to quit" (`screen_trim.go:230`) but trim has no `q` case and an input is always focused, so `q` types a "q". Also :163 duplicates the `probeTimeout` constant as a bare `30*time.Second`.
- **`pixDepth` misses non-planar formats** (`hdr.go:87-97`): `gray10le`, `rgb48*`, `y210`, `x2rgb10le` report 8-bit, so those sources get crushed without a warning. Add a trailing-digit fallback.
- **Dead code:** `nav.go replace()` is never called; `hdrWarner.probing` is write-only (`hdr.go:21,28,49`).
- **Step-back asymmetries:** tracks Esc from confirm/output jumps two levels to "tracks" (`screen_tracks.go:113-117`); metadata "Edit Tags" has no editable output step while Strip does (`screen_metadata.go:272-295`); compress Esc from output always goes to "speed", losing the custom-CRF context (compress/effects/resize handle this differently — compress is the odd one).
- **Output-path resolution is copy-pasted ~9×** across wizards (the trim → IsAbs → Join block and the warn/err view fragments) — one shared helper would prevent future drift.
- **Small terminals:** `viewport.New(msg.Width-4, msg.Height-9)` (`screen_exec.go:141`) and `Height-6` list sizing go negative; `tracksView` renders unscrollable raw lines (`screen_tracks.go:292-320`).
- **Cosmetic:** batch shows "File 0/N:" before the first progress msg and "Done." header on total failure (`screen_batch.go:454-475`); join's VFR warning compares raw `"30000/1001"` strings (`screen_join.go:398`); `newTimeInput` builds CRF/crop/tag fields despite its name; media list misses `.mka`/`.m2ts`/`.ogv`; `os.Getwd()` error ignored ×3 in main menu.
- **Nil-guard inconsistency:** 5 screens call `cfg.Prober` unguarded while 6 others nil-check it. Latent (main always sets it) — pick one convention.

## Test coverage gaps (unit tier)

- `internal/ffprobe` has **zero unit tests** (arg construction, JSON parse failure, `N/A` filtering) — inject a fake Runner.
- `screen_extract_audio.go` has one Esc test; no happy-path/no-audio-path test (every other wizard has one). `CheckFFmpegFFprobe`, main menu/action menu/inspect models, and `root.New` path routing are untested.
- Trim's unit test never asserts that `-ss`/`-to` sit **after** `-i` — the ffmpeg-4.x contract that's the whole point — is only pinned by ordering implicitly. Add the index assertion.
- GIF batch args are barely asserted (`fps=15`, `palettegen` unpinned — the unit test would pass with `palettegen` replaced by anything); batch mp4 CRF presets, mkv/mov/avi copy, mp3/flac targets untested.
- **execx has no Windows tests at all** despite goreleaser shipping windows/amd64+arm64 (`//go:build unix`); `proc_windows.go`'s no-op cancel is unverified.
- Integration gaps: reverse-without-audio, 4x atempo chain, rotate 180/270, flip-v, batch mp3/flac, hvc1 tag verification, and chapter-strip verification (`-show_chapters`) are never executed end-to-end.

## Notes / verified non-issues

The hard parts are genuinely right: overwrite guard covers every exec push; ctrl+c cancels ffmpeg via process groups + `WaitDelay` before quitting; exec/batch channels drain safely with no goroutine leaks; trim keyframe snapping _is_ proven by a real integration test; DTS fixture honesty matches docs; Docker/npm workflows line up with goreleaser's outputs (except M4/M5). The fork-history items (LICENSE notice, restarted history) are intentional per AGENTS.md.

**Suggested priority:** H1 (3-line fix, user-visible data-write bug) → H2/M1 (multi-stream + bitmap-sub behavior) → M2/M3/M4/M6 → the rest as you touch each file.

---
