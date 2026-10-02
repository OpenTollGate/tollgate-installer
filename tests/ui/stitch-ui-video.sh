#!/bin/sh
# Stitch the three recorded acts into one labelled mp4.
#
# Playwright records one video per page; the run opens three pages (Act 1 decline,
# Act 2 confirm, Act 3 remembered), so the proof is three clips in order. Each
# gets a caption burned in, because an unlabelled screen recording of a wizard
# makes the viewer guess which act they are watching.
set -e
DIR="$1"
FONT="${FONT:-/usr/share/texlive/texmf-dist/fonts/truetype/public/dejavu/DejaVuSans.ttf}"
[ -f "$FONT" ] || FONT=/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf

cd "$DIR"
rm -f act_*.mp4 list.txt ui-deploy-trust.mp4

i=0
for f in $(ls -tr *.webm); do
    i=$((i + 1))
    case $i in
        1) LABEL='Act 1 - Deploy asks ONCE and names the fingerprint. Declining cancels it.' ;;
        2) LABEL='Act 2 - Confirmed once. The install starts. No separate Trust button.' ;;
        3) LABEL='Act 3 - Same router on a fresh page. NO prompt. Already remembered.' ;;
        *) LABEL="Act $i" ;;
    esac
    ffmpeg -y -loglevel error -i "$f" \
        -vf "drawtext=fontfile=${FONT}:text='${LABEL}':x=18:y=16:fontsize=24:fontcolor=white:box=1:boxcolor=0x0b0f14@0.85:boxborderw=12" \
        -c:v libx264 -pix_fmt yuv420p -r 25 "act_$(printf '%02d' $i).mp4"
    echo "file '$DIR/act_$(printf '%02d' $i).mp4'" >> list.txt
    echo "act $i <- $f"
done

ffmpeg -y -loglevel error -f concat -safe 0 -i list.txt -c copy -movflags +faststart ui-deploy-trust.mp4
ffprobe -v error -show_entries format=duration,size -of default=nw=1 ui-deploy-trust.mp4
ls -la ui-deploy-trust.mp4
