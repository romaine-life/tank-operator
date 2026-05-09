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

Zoom applies to all open Tank windows and persists across app restarts.

## Package And Install

```powershell
npm run dist
```

The installer lands at `desktop/dist/Tank-Operator-Setup-<version>.exe`.
It installs per-user by default, creates a Start Menu shortcut, offers a
Desktop shortcut, and launches Tank after setup completes.
The Windows executable is post-processed with `rcedit` so Start Menu and
taskbar pins use the Tank icon instead of Electron's default icon.

For unattended installs, use any of:

```powershell
.\dist\Tank-Operator-Setup-0.1.3.exe /unattended
.\dist\Tank-Operator-Setup-0.1.3.exe /quiet
.\dist\Tank-Operator-Setup-0.1.3.exe /S
```

`/unattended`, `/quiet`, `/silent`, `--unattended`, `--quiet`, and `--silent`
are aliases for NSIS silent mode. `/currentuser` and `/allusers` still work
for install scope when needed.

For unattended uninstall:

```powershell
& "$env:LOCALAPPDATA\Programs\Tank Operator\Uninstall Tank Operator.exe" /unattended
```

For a quick unpacked app without installing:

```powershell
npm run pack
.\dist\win-unpacked\"Tank Operator.exe"
```
