# Tank Operator Desktop

Thin Electron shell for the hosted Tank Operator frontend.

## Run

```powershell
cd desktop
npm ci
npm start
```

By default the shell opens `https://tank.romaine.life`. To point it at another
Tank origin:

```powershell
$env:TANK_OPERATOR_URL = "https://tank.romaine.life"
npm start
```

The shell keeps Tank, Microsoft sign-in, and GitHub install navigation inside
the app window. Links opened from the terminal are sent to the system browser.

## Shortcuts

- `Ctrl+R` / `F5`: reload Tank.
- `Ctrl+Shift+R`: reload Tank without cache.
- `Ctrl++` / `Ctrl+=`: zoom in.
- `Ctrl+-`: zoom out.
- `Ctrl+0`: reset zoom.
- `Alt+Left` / `Alt+Right`: navigate back and forward.

## Package And Install

```powershell
npm run dist
```

The installer lands at `desktop/dist/Tank-Operator-Setup-<version>.exe`.
It installs per-user by default, creates a Start Menu shortcut, offers a
Desktop shortcut, and launches Tank after setup completes.
The Windows executable is post-processed with `rcedit` so Start Menu and
taskbar pins use the Tank icon instead of Electron's default icon.

For a quick unpacked app without installing:

```powershell
npm run pack
.\dist\win-unpacked\"Tank Operator.exe"
```
