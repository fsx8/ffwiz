#!/usr/bin/env bash
#
# Generate ffwiz test/demo media fixtures (deterministic lavfi sources).
#
#   scripts/testmedia.sh [output-dir]     (default: ./testmedia)
#
# Requires ffmpeg + ffprobe in PATH. The fixtures are intentionally tiny
# (seconds long, low bitrate) so generation finishes in well under a minute.
# They power the integration test suite (go test -tags=integration ./...)
# and docs/demo.tape.

set -euo pipefail

OUT="${1:-testmedia}"
mkdir -p "$OUT"

FF="ffmpeg -hide_banner -loglevel error -y"
X264="libx264 -preset ultrafast -crf 32 -pix_fmt yuv420p -g 48"

echo "Generating fixtures in $OUT ..."

# ---------------------------------------------------------------- core video
# basic.mp4 — h264 + aac, 640x360@24, 48 kHz, 20 s. Main demo + test file.
$FF -f lavfi -i testsrc2=size=640x360:rate=24:duration=20 \
    -f lavfi -i "sine=frequency=440:sample_rate=48000:duration=20" \
    -c:v $X264 -c:a aac -b:a 128k -shortest "$OUT/basic.mp4"
echo "  basic.mp4             h264+aac 640x360 20s"

# noaudio.mp4 — video only (extract-audio error path, join silence synth).
$FF -f lavfi -i testsrc2=size=640x360:rate=24:duration=6 \
    -c:v $X264 "$OUT/noaudio.mp4"
echo "  noaudio.mp4           h264 video-only 6s"

# ---------------------------------------------------------------- audio only
$FF -f lavfi -i "sine=frequency=440:sample_rate=44100:duration=6" \
    -c:a libmp3lame -b:a 128k "$OUT/audio_only.mp3"
$FF -f lavfi -i "sine=frequency=440:sample_rate=48000:duration=6" \
    -c:a flac "$OUT/audio_only.flac"
$FF -f lavfi -i "sine=frequency=440:sample_rate=48000:duration=6" \
    -c:a pcm_s16le "$OUT/audio_only.wav"
echo "  audio_only.{mp3,flac,wav}"

# ------------------------------------------------------------ special layout
# multiaudio.mkv — one video + two tagged audio tracks (track editor).
$FF -f lavfi -i testsrc2=size=640x360:rate=24:duration=6 \
    -f lavfi -i "sine=frequency=440:sample_rate=48000:duration=6" \
    -f lavfi -i "sine=frequency=880:sample_rate=48000:duration=6" \
    -map 0:v -map 1:a -map 2:a \
    -c:v $X264 -c:a aac -b:a 96k \
    -metadata:s:a:0 language=eng -metadata:s:a:1 language=deu \
    -shortest "$OUT/multiaudio.mkv"
echo "  multiaudio.mkv        1 video + 2 audio (eng/deu)"

# subs.srt + subs.mkv — embedded subtitle stream (track editor, mp4 subs).
cat > "$OUT/subs.srt" <<'EOF'
1
00:00:00,500 --> 00:00:02,000
Hello from ffwiz test fixtures

2
00:00:02,500 --> 00:00:05,500
A subtitle track for testing
EOF
$FF -f lavfi -i testsrc2=size=640x360:rate=24:duration=6 \
    -f lavfi -i "sine=frequency=440:sample_rate=48000:duration=6" \
    -i "$OUT/subs.srt" \
    -map 0:v -map 1:a -map 2:s \
    -c:v $X264 -c:a aac -b:a 96k -c:s srt \
    -shortest "$OUT/subs.mkv"
echo "  subs.mkv (+subs.srt)  1 video + 1 audio + 1 srt subtitle"

# ------------------------------------------------------------ codec variety
$FF -f lavfi -i testsrc2=size=640x360:rate=24:duration=4 \
    -c:v libx265 -preset ultrafast -crf 35 -x265-params log-level=error \
    "$OUT/hevc.mkv"
$FF -f lavfi -i testsrc2=size=640x360:rate=24:duration=4 \
    -f lavfi -i "sine=frequency=440:sample_rate=48000:duration=4" \
    -c:v libvpx-vp9 -deadline good -cpu-used 8 -crf 40 -b:v 0 \
    -c:a libopus -b:a 96k -shortest "$OUT/vp9.webm"
$FF -f lavfi -i testsrc2=size=640x360:rate=24:duration=4 \
    -f lavfi -i "sine=frequency=440:sample_rate=44100:duration=4" \
    -c:v mpeg4 -q:v 7 -c:a libmp3lame -b:a 128k -shortest "$OUT/mpeg4.avi"
echo "  hevc.mkv vp9.webm mpeg4.avi"

# ---------------------------------------------------------------- join pair
# Different resolution, fps, sample rate and channel count: the join wizard
# must normalize everything to the first input's targets.
$FF -f lavfi -i testsrc2=size=640x480:rate=30:duration=5 \
    -f lavfi -i "sine=frequency=440:sample_rate=44100:duration=5" \
    -c:v $X264 -c:a aac -b:a 96k -ac 1 -shortest "$OUT/join_a.mp4"
$FF -f lavfi -i testsrc2=size=1280x720:rate=25:duration=5 \
    -f lavfi -i "sine=frequency=880:sample_rate=48000:duration=5" \
    -c:v $X264 -c:a aac -b:a 128k -shortest "$OUT/join_b.mp4"
echo "  join_a.mp4 join_b.mp4  mixed res/fps/sample-rate/channels"

# ---------------------------------------------------------------- long file
# long.mp4 — 60 s small video for trim + live-progress tests.
$FF -f lavfi -i testsrc2=size=320x180:rate=12:duration=60 \
    -f lavfi -i "sine=frequency=440:sample_rate=48000:duration=60" \
    -c:v $X264 -c:a aac -b:a 64k -shortest "$OUT/long.mp4"
echo "  long.mp4              320x180 60s (trim/progress)"

# ----------------------------------------------------------------- chapters
# chapters.mkv — basic.mp4 re-muxed with two chapters: the metadata strip
# must drop them, tag edits must retain them.
cat > "$OUT/chapters.txt" <<'EOF'
;FFMETADATA1
[CHAPTER]
TIMEBASE=1/1000
START=0
END=10000
title=Part One
[CHAPTER]
TIMEBASE=1/1000
START=10000
END=20000
title=Part Two
EOF
$FF -i "$OUT/basic.mp4" -i "$OUT/chapters.txt" -map 0 -map_chapters 1 -c copy "$OUT/chapters.mkv"
echo "  chapters.mkv          basic.mp4 + 2 chapters (metadata strip)"

# ------------------------------------------------------- uhd hdr "feature"
# hdr4k.mkv — 4K HEVC 10-bit HDR (HDR10 signaling), three audio layers
# (DTS 5.1, EAC3 5.1, AAC stereo commentary) and four subtitle tracks
# (srt x3, ass). The DTS encoder is core-only and experimental: ffmpeg
# cannot encode DTS-HD MA, but codec_name=dts drives the same conversion
# path in ffwiz (DTS family -> EAC3 for device compatibility).
# The HDR signaling is laid in via the setparams filter, NOT the -color_*
# output flags: since ffmpeg 7.1 those no longer reach the muxer, while
# filter side data is muxed on every version (6.1 included).
cat > "$OUT/hdr4k_en.srt" <<'EOF'
1
00:00:00,500 --> 00:00:02,000
English subtitle line

2
00:00:02,500 --> 00:00:03,900
Second english line
EOF
cat > "$OUT/hdr4k_de.srt" <<'EOF'
1
00:00:00,500 --> 00:00:02,000
Deutsche Untertitelzeile

2
00:00:02,500 --> 00:00:03,900
Zweite Zeile
EOF
cat > "$OUT/hdr4k_es.srt" <<'EOF'
1
00:00:00,500 --> 00:00:02,000
Subtítulo en español

2
00:00:02,500 --> 00:00:03,900
Segunda línea
EOF
cat > "$OUT/hdr4k_en.ass" <<'EOF'
[Script Info]
ScriptType: v4.00+

[V4+ Styles]
Format: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, Alignment, MarginL, MarginR, MarginV, Encoding
Style: Default,Arial,18,&H00FFFFFF,&H000000FF,&H00000000,&H00000000,0,0,0,0,100,100,0,0,1,1,0,2,10,10,10,1

[Events]
Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text
Dialogue: 0,0:00:00.50,0:00:02.00,Default,,0,0,0,,ASS styled subtitle
Dialogue: 0,0:00:02.50,0:00:03.90,Default,,0,0,0,,Second styled line
EOF
$FF -f lavfi -i "testsrc2=size=3840x2160:rate=24:duration=4" \
    -f lavfi -i "sine=frequency=440:sample_rate=48000:duration=4" \
    -f lavfi -i "sine=frequency=880:sample_rate=48000:duration=4" \
    -f lavfi -i "sine=frequency=220:sample_rate=48000:duration=4" \
    -i "$OUT/hdr4k_en.srt" -i "$OUT/hdr4k_de.srt" -i "$OUT/hdr4k_es.srt" -i "$OUT/hdr4k_en.ass" \
    -map 0:v -map 1:a -map 2:a -map 3:a -map 4:s -map 5:s -map 6:s -map 7:s \
    -vf "setparams=color_primaries=bt2020:color_trc=smpte2084:colorspace=bt2020nc,format=yuv420p10le" \
    -c:v libx265 -preset ultrafast -crf 40 -g 48 \
    -x265-params "profile=main10:log-level=error" \
    -filter:a:0 "aformat=channel_layouts=5.1" -c:a:0 dts -strict -2 -b:a:0 768k \
    -filter:a:1 "aformat=channel_layouts=5.1" -c:a:1 eac3 -b:a:1 384k \
    -filter:a:2 "aformat=channel_layouts=stereo" -c:a:2 aac -b:a:2 128k \
    -c:s:0 srt -c:s:1 srt -c:s:2 srt -c:s:3 ass \
    -metadata:s:a:0 language=eng -metadata:s:a:0 title="DTS 5.1" \
    -metadata:s:a:1 language=deu -metadata:s:a:1 title="EAC3 5.1" \
    -metadata:s:a:2 language=eng -metadata:s:a:2 title="Commentary" \
    -metadata:s:s:0 language=eng \
    -metadata:s:s:1 language=deu \
    -metadata:s:s:2 language=spa \
    -metadata:s:s:3 language=eng \
    -shortest "$OUT/hdr4k.mkv"
echo "  hdr4k.mkv (+4 subtitle files)  3840x2160 HEVC HDR10, dts+eac3+aac, 4 subs"

# ------------------------------------------------------- small hdr fixture
# hdr_small.mkv — fast HDR10 sample for the HDR re-encode integration
# tests: testsrc2 480x270, 10 s, 10-bit HEVC. The HDR signaling is laid in
# via the setparams filter, NOT the -color_* output flags: since ffmpeg
# 7.1 those no longer reach the muxer, while filter side data is muxed on
# every version. x265 additionally writes a matching mastering-display SEI
# (hdr10 params), and a stereo AAC track feeds the speed/reverse paths.
$FF -f lavfi -i "testsrc2=size=480x270:rate=24:duration=10" \
    -f lavfi -i "sine=frequency=440:sample_rate=48000:duration=10" \
    -vf "setparams=color_primaries=bt2020:color_trc=smpte2084:colorspace=bt2020nc,format=yuv420p10le" \
    -c:v libx265 -preset medium -crf 22 -g 48 \
    -x265-params "hdr10=1:master-display=G(8500,39850)B(6550,2300)R(35400,14600)WP(15635,16450)L(10000000,1):max-cll=1000,400:log-level=error" \
    -ac 2 -c:a aac -b:a 96k \
    -shortest "$OUT/hdr_small.mkv"
echo "  hdr_small.mkv         480x270 HEVC 10-bit HDR10 + aac, 10s"

echo "Done. $(ls "$OUT" | wc -l) files in $OUT"
