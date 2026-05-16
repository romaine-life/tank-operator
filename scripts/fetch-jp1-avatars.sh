#!/usr/bin/env bash
# Run from inside the tank-operator repo on the jp1-avatars branch.
# Downloads the 13 JP1 avatar source images into
# frontend/public/assets/avatars/jp1-<slug>.png.
#
# Sources: Jurassic Park Fandom wiki (static.wikia.nocookie.net). These
# are studio-owned promo/screencap images, used locally as decorative
# avatars for a single-operator personal app. See
# frontend/public/assets/avatars/ATTRIBUTION.md.
#
# Fandom's CDN serves WEBP regardless of the URL extension; many of these
# requests land as WEBP bytes saved into a .png-named file. That's fine
# as an intermediate — scripts/normalize-jp1-avatars.py converts every
# slug to a true 256x256 PNG with subject-centred crop. Run that next.

set -euo pipefail

DEST="frontend/public/assets/avatars"
if [[ ! -d "$DEST" ]]; then
  echo "Run this from the repo root (expected $DEST to exist)." >&2
  exit 1
fi

UA="Mozilla/5.0 (compatible; tank-operator-personal-app/1.0)"

declare -A URLS=(
  # Dinos
  [jp1-trex]="https://static.wikia.nocookie.net/jurassicpark/images/a/aa/Rexy.jpg/revision/latest?cb=20200605165953"
  [jp1-raptor]="https://static.wikia.nocookie.net/jurassicpark/images/7/72/BigOne04.jpg/revision/latest?cb=20120804203143"
  [jp1-brachiosaurus]="https://static.wikia.nocookie.net/jurassicpark/images/e/e9/JP-Brachiosaur.jpg/revision/latest?cb=20090416060101"
  [jp1-dilophosaurus]="https://static.wikia.nocookie.net/jurassicpark/images/a/a7/Dilophosaurus_Open_Frills.png/revision/latest?cb=20240214114411"
  [jp1-triceratops]="https://static.wikia.nocookie.net/jurassicpark/images/e/ed/JPI_Triceratops.png/revision/latest?cb=20200525233206"
  [jp1-gallimimus]="https://static.wikia.nocookie.net/jurassicpark/images/8/86/Trexkillinggallijp1.jpg/revision/latest?cb=20160514230213"
  # Humans
  [jp1-grant]="https://static.wikia.nocookie.net/jurassicpark/images/7/79/Alan_Grant_1993.png/revision/latest?cb=20241117015123"
  [jp1-sattler]="https://static.wikia.nocookie.net/jurassicpark/images/1/1f/Ellie_Sattler_1993.jpg/revision/latest?cb=20241117015654"
  [jp1-malcolm]="https://static.wikia.nocookie.net/jurassicpark/images/3/3c/Ian_Malcolm_1993.png/revision/latest?cb=20240123021856"
  [jp1-hammond]="https://static.wikia.nocookie.net/jurassicpark/images/2/29/John_Hammond_1993.jpg/revision/latest?cb=20240209055500"
  # Iconic scene: Nedry at his computer workstation — sneaky grin, glasses,
  # headset, Mr. DNA papers and the famous red-LED display behind him.
  # Staged locally; the wiki only has the dock scene.
  [jp1-nedry]="local:scripts/sources/jp1-nedry-source.png"
  # Iconic scene: Muldoon's "Clever girl" — over-the-shoulder framing
  # from the meme frame. Not on the wiki at this composition, so the
  # source is staged locally; the curl fetch is replaced with a cp below.
  [jp1-muldoon]="local:scripts/sources/jp1-muldoon-source.png"
  # Iconic scene: Arnold's "Hold on to your butts" — Samuel L. Jackson
  # in the dim control room, glasses pushed up on his forehead, cigarette
  # in the corner of his mouth. Staged locally; no wiki file at this
  # exact framing/lighting.
  [jp1-arnold]="local:scripts/sources/jp1-arnold-source.png"
)

for slug in "${!URLS[@]}"; do
  out="$DEST/${slug}.png"
  src="${URLS[$slug]}"
  if [[ "$src" == local:* ]]; then
    local_path="${src#local:}"
    echo "copy $slug <- $local_path"
    cp "$local_path" "$out"
  else
    echo "fetch $slug -> $out"
    curl -sSL -A "$UA" "$src" -o "$out"
  fi
done

echo
echo "Done. ${#URLS[@]} source images in $DEST. Now run:"
echo "  python3 scripts/normalize-jp1-avatars.py"
echo "to crop + re-encode each to a true 256x256 PNG."
