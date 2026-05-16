#!/usr/bin/env python3
"""Crop + re-encode the JP1 avatar source images to true 256×256 PNGs.

Fandom's CDN serves WEBP regardless of the URL extension, so the .png
files written by ``fetch-jp1-avatars.sh`` are actually WEBP bytes. This
script decodes them, applies a per-slug focal-point crop hint so wide
scene stills don't lose their subject when squared, resizes to 256×256,
and overwrites each ``frontend/public/assets/avatars/jp1-<slug>.png``
with a real PNG.

Run from the repo root after ``bash scripts/fetch-jp1-avatars.sh``.
Requires Pillow (``pip install pillow`` or ``uv pip install pillow``).
"""

from __future__ import annotations

import glob
import os

from PIL import Image, ImageOps

SRC_DIR = "frontend/public/assets/avatars"
TARGET = 256

# (x_frac, y_frac): where the centre of interest sits in the source
# image, as a fraction of (width, height). 0.5 is the centre. Anything
# omitted defaults to (0.5, 0.5).
#
# Tuned for the current 14-slug set — re-tune if you swap source URLs.
HINTS: dict[str, tuple[float, float]] = {
    # Arnold "Hold on to your butts" (1280×720): face on the right
    # two-thirds. Bias the square crop right so the face dominates.
    "jp1-arnold": (0.62, 0.42),
    # Nedry at his workstation (918×687): face slightly left of centre.
    "jp1-nedry": (0.46, 0.50),
    # Brachiosaurus — keep more head/neck, less ground.
    "jp1-brachiosaurus": (0.50, 0.45),
    # Muldoon close-up (1491×1305): face sits upper-left, lots of shirt
    # bottom-right. Bias toward the face so the square crop keeps it.
    "jp1-muldoon": (0.40, 0.35),
}

# Explicit pre-resize crop rectangles for slugs where the (x_frac, y_frac)
# hint isn't expressive enough. (left, top, right, bottom) — applied
# *before* the square crop/resize stages. Skip the HINTS step when present.
#
# Tuned to the current source images in scripts/sources/ — re-tune if you
# swap.
CROPS: dict[str, tuple[int, int, int, int]] = {}


def square_crop(im: Image.Image, hint: tuple[float, float]) -> Image.Image:
    w, h = im.size
    side = min(w, h)
    cx = int(w * hint[0])
    cy = int(h * hint[1])
    half = side // 2
    left = max(0, min(w - side, cx - half))
    top = max(0, min(h - side, cy - half))
    return im.crop((left, top, left + side, top + side))


def main() -> int:
    if not os.path.isdir(SRC_DIR):
        print(f"missing {SRC_DIR} — run this from the repo root.")
        return 1
    paths = sorted(glob.glob(f"{SRC_DIR}/jp1-*.png"))
    if not paths:
        print(f"no jp1-*.png files in {SRC_DIR} — run fetch-jp1-avatars.sh first.")
        return 1
    for src in paths:
        slug = os.path.splitext(os.path.basename(src))[0]
        im = Image.open(src)
        im = ImageOps.exif_transpose(im).convert("RGBA")
        explicit = CROPS.get(slug)
        if explicit is not None:
            cropped = im.crop(explicit)
        else:
            hint = HINTS.get(slug, (0.5, 0.5))
            cropped = square_crop(im, hint)
        resized = cropped.resize((TARGET, TARGET), Image.LANCZOS)
        resized.save(src, format="PNG", optimize=True)
        print(f"  {slug:24s} -> {TARGET}x{TARGET} PNG ({os.path.getsize(src):>7d} bytes)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
