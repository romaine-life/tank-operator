# Avatar Assets

Tank Operator's assistant avatar pool is the JP1 cast (2 dinos + 7 humans).
Files are named `jp1-<slug>.png`; the slug matches the `id` field in
`frontend/src/sessionAvatars.tsx → AGENT_AVATARS`.

Source: stills and promotional art from *Jurassic Park* (1993, Universal
Pictures / Amblin Entertainment). These are unlicensed studio assets used
locally as decorative session avatars for a single-operator personal app.
Do not redistribute these files outside this repo's local install context.

Asset notes:

- Square 256×256 PNG, true PNG encoding (Fandom's CDN serves WEBP under
  `.png` URLs; `scripts/normalize-jp1-avatars.py` re-encodes).
- Subject-centred crop per `HINTS` in the normalize script — wide scene
  stills (e.g. Muldoon's "clever girl" or Nedry on the dock) get a
  positional hint so the focal point survives the square crop.
- Rendered through `.session-avatar` / `.run-status-avatar` /
  `.run-msg-ai-icon` (see `frontend/src/index.css`) at 24–42px with
  `object-fit: contain` over a translucent backdrop.

Reproducing the asset set:

```sh
bash scripts/fetch-jp1-avatars.sh
python3 scripts/normalize-jp1-avatars.py
```

History:

- The prior pool was 4 SVGs (Noto Emoji + Twemoji sauropod/T-Rex) from
  `googlefonts/noto-emoji` (Apache 2.0) and `twitter/twemoji` (CC BY 4.0).
  Replaced in the JP1 avatar pass.
