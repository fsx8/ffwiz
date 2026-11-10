# ffwiz Project Context

## Project Overview

**ffwiz** (ffmpeg wizard) is a terminal UI (TUI) built on top of FFmpeg. It provides an intuitive, menu-driven interface for common audio and video manipulation tasks without needing to memorize FFmpeg commands. ffwiz is a standalone Go project (it originated as a fork of a Python tool; the history was restarted and the fork network left — MIT `LICENSE` intentionally retains the original copyright notice).

### Key Features

- **Inspect Media Properties**: Detailed information about video and audio streams
- **Join Videos**: Concatenate multiple videos with automatic resolution and sample rate handling
- **Trim Videos**: Lossless cutting (`-c copy` + `-map 0` so every stream survives, output-side `-ss`/`-to` for ffmpeg 4.x compat)
- **Extract Audio**: Rip audio tracks to mp3/flac/wav
- **Interactive Conversion**: Granular per-track control (keep/remove/convert with codec selection)
- **Batch Conversion**: Convert a whole directory with quality presets (incl. 2-step GIF palette); the copy preset maps every stream and normalizes per target container (mkv copies everything, mp4/mov re-encode unmuxable audio to AAC and text subs to mov_text while dropping bitmap subs, avi/webm fall back to a re-encode)
- **Video Editing**: resize (presets/custom, `-2` auto dimension), crop, rotate/flip (transform.go), compress (CRF + x264 preset), speed (setpts + per-track atempo chain, 0.25–4x), reverse, lossless mute (effects.go) — speed/reverse process every probed audio track, not just the first; Modify Tracks takes bulk Shift+K/R/C (keep/remove all, convert every track of the cursor type via one codec prompt) and shows track titles, truncated so each track stays one line
- **HDR preservation**: the re-encode features (compress, resize, transform, speed/reverse, Modify Tracks convert to libx265) keep HDR/10-bit sources intact — 10-bit HEVC (`libx265`) with the source's color signaling re-asserted by a `setparams` filter segment (portable: the `-color_*` output flags no longer reach the muxer on ffmpeg 7.1+/8.x, filter side data does on every version) plus `format=yuv420p10le`, and probed mastering-display/CLL side data forwarded to `-x265-params hdr10=1:master-display=...:max-cll=...` when ffprobe surfaces it (omitted gracefully otherwise). Detection (`pixDepth > 8` or an HDR transfer) lives in `internal/ffmpeg/hdr.go` so generators and UI agree; the wizards show an informational note instead of the old crushing warning. Batch re-encode presets and non-x265 track conversions stay 8-bit and keep the warning
- **Screenshots**: single-frame grab at a timestamp (input-side `-ss`, png/jpg)
- **Metadata Editor**: lossless tag edit (`-metadata`, keys sorted for determinism) and full strip (`-map_metadata -1 -map_chapters -1` plus per-stream tag clears; `-map 0` is required so ffmpeg's auto-selection does not drop "non-best" streams)
- **Live Progress Bar**: ffmpeg runs with `-nostats -progress pipe:1`; stdout lines are parsed (`internal/ffmpeg/progress.go`) against ffprobe-derived total duration and rendered as a percent bar with ETA

### Architecture

- `cmd/ffwiz/`: Application entrypoint (flag parsing, `-version`, `-path`)
- `internal/app/`: Bubble Tea screens and navigation (stack-based: menus + wizards + exec view)
- `internal/ffmpeg/`: Deterministic FFmpeg argument generation (one file per feature, no subprocesses here)
- `internal/ffprobe/`: ffprobe JSON parsing and probing
- `internal/execx/`: Process execution (buffered + streaming stdout/stderr with per-line callbacks, context-aware cancellation)
- `internal/media/`: Media file discovery helpers

### Technologies

- Go (see `go.mod` for the exact minimum version)
- FFmpeg/ffprobe as external runtime dependencies (not bundled)
- bubbletea, bubbles, lipgloss ( Charm )

## Building and Running

### Prerequisites

- Go toolchain (version per `go.mod`)
- FFmpeg + ffprobe in PATH (ffprobe ships with ffmpeg)

### From Source

```bash
git clone https://github.com/fsx8/ffwiz.git
cd ffwiz
go run ./cmd/ffwiz            # or: go build ./cmd/ffwiz
```

- Optional arg: `ffwiz [file_or_folder]` opens the action menu / join wizard directly
- `-version` prints the version (goreleaser ldflags, or module build info via `go install`)

## Development Conventions

### Git

- **Default branch: `main`** — the only development branch
- **`gh-pages` is auto-generated** (hosts the apt repository); never edit manually
- Commit identity must be `fsx8 <fsx8@users.noreply.github.com>` (already set in this repo's local git config — do not override with a global/other identity)
- History was deliberately restarted (single root commit); do not re-introduce old refs

### Code Style

- Core FFmpeg argument generation lives in `internal/ffmpeg/` and must stay deterministic (pure functions -> `Cmd{Args}`)
- UI flows live in `internal/app/` on Bubble Tea components; navigation via `push`/`pop`/`replace` cmds in `nav.go`
- All subprocess execution goes through `internal/execx` and must respect context cancellation (Ctrl+C/Esc must never orphan a running ffmpeg)

### Error Handling

- Errors are logged to `ffmpeg_log.txt` in the working directory
- Ctrl+C cancels in-flight ffmpeg; screens show streamed stderr in a viewport
- Progress flags are added at execution sites via `ffmpeg.AddProgressArgs`, never inside the deterministic generators themselves

### Testing

Two-tier contract: unit tests assert on generated arguments and model state without executing anything; integration tests run real ffmpeg, then ffprobe the written output files and assert stream layout (codecs, counts, channels, durations, pix_fmt, tags) — a feature claim is only trusted when proven by the integration tier.

- `go test ./...` — deterministic command/argument generation tests; no FFmpeg execution
- `go test -tags=integration ./...` — end-to-end tests that run real ffmpeg/ffprobe through every feature (trim, join, track editing, batch/GIF, progress parsing) plus a remux scenario matrix over `hdr4k.mkv` (remove/convert audio, keep/remove subtitles, metadata/tag preservation, mkv→mp4/webm container adaptation); they self-skip when tools or fixtures are missing
- `scripts/testmedia.sh` — regenerates the deterministic fixture set into `testmedia/` (gitignored; lavfi sources, ~25s to build). Includes `hdr4k.mkv`: 4K HEVC 10-bit HDR10 with DTS 5.1 + EAC3 5.1 + AAC and 4 subtitle tracks, plus `hdr_small.mkv`: 480x270 HEVC 10-bit HDR10 + stereo AAC for the fast HDR re-encode tests (both lay the HDR signaling in via the `setparams` filter, never the `-color_*` flags). Note: ffmpeg can only ENCODE DTS core (`-strict -2`, experimental encoder) — DTS-HD MA is decode-only upstream; a true HD-MA sample would have to be supplied manually
- CI: `.github/workflows/tests.yml` runs both tiers on every push/PR (installs ffmpeg + generates fixtures)
- Before handing back work: `gofmt -l .`, `go vet ./...`, `go build ./...`, `go test ./...`

## Release & Distribution

Releasing = push a `v*` tag on `main`. goreleaser (`.goreleaser.yaml`) then builds everything; downstream workflows pick up the rest automatically.

| Channel | Mechanism |
|---|---|
| Binaries / `.deb` / `.rpm` | goreleaser -> GitHub Release assets (11 files) |
| Homebrew (macOS) | goreleaser `homebrew_casks` -> `fsx8/homebrew-tap` (needs `HOMEBREW_TAP_TOKEN` secret) |
| Docker | goreleaser `dockers_v2` -> `ghcr.io/fsx8/ffwiz` (multi-arch, ffmpeg bundled, `Dockerfile`) |
| apt (Ubuntu/Debian servers) | `apt.yml` workflow auto-runs on Release completion (`workflow_run` trigger — `on: release` does NOT fire because goreleaser publishes with `GITHUB_TOKEN`), signs and pushes the repo to `gh-pages` (needs `APT_GPG_PRIVATE_KEY` secret) |
| npm | `npm.yml` auto-runs on Release completion (`workflow_run` — prereleases are skipped so `latest` never points at one); `workflow_dispatch` kept for retries (never re-publishes an existing version — registry is immutable). Uses OIDC **trusted publishing** (no npm token); stages README+LICENSE into `npm/` and rewrites relative links; registered publisher: fsx8/ffwiz, workflow `npm.yml` |
| go install | works automatically via the module path `github.com/fsx8/ffwiz/cmd/ffwiz` |

One-line installer (served from Pages): `curl -fsSL https://fsx8.github.io/ffwiz/install.sh | sudo bash` — source in `scripts/install.sh`, re-copied to `gh-pages` by `apt.yml` on each release.

Version injection: goreleaser sets `main.version` via ldflags; `go install` builds fall back to module build info (see `effectiveVersion()` in `cmd/ffwiz/main.go`).

## Key Files and Directories

- `go.mod` / `go.sum`: module `github.com/fsx8/ffwiz` and dependencies
- `.goreleaser.yaml`: builds, archives, nfpms, homebrew cask, docker
- `.github/workflows/`: `release.yml` (goreleaser), `apt.yml` (apt repo), `npm.yml` (npm publish), `tests.yml` (CI)
- `scripts/install.sh`: apt one-line installer
- `npm/`: npm wrapper package (binary-downloading launcher)
- `Dockerfile`: container image (used by goreleaser with prebuilt binaries)
- `gh-pages` branch: published apt repository (`apt/`, `KEY.gpg`, `install.sh`) — generated

## Important Notes

1. FFmpeg must be installed separately on the user's system (except in the Docker image, which bundles it)
2. The app creates `ffmpeg_log.txt` in the working directory each session
3. Trim uses output-side seeking deliberately — do not move `-ss`/`-to` before `-i` (breaks ffmpeg 4.x, e.g. Ubuntu 22.04). Because `-c copy` can only start a video at a keyframe, the trim wizard probes keyframes (`ffprobe -skip_frame nokey`, `ffprobe.Prober.Keyframes`) and snaps the requested start to the previous keyframe (`ffmpeg.SnapToKeyframe`, ~50 ms tolerance); a mid-GOP cut therefore begins up to one GOP earlier than requested instead of dropping video packets — without keyframe data the trim runs unsnapped (graceful fallback). Pinned by `TestTrimMidGOPSnapsToPreviousKeyframe`
4. Registry immutability: fixing a broken npm package or README requires a new version tag, never a force-publish
