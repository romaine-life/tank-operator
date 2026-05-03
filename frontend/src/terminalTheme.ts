import type { ITheme } from "@xterm/xterm";

export const TERMINAL_BACKGROUND = "#171717";
export const TERMINAL_FOREGROUND = "#e4e4e4";

export const ANSI_STANDARD_OVERRIDES: Record<number, string> = {
  // Claude Code's current startup screen emits standard red for the logo and
  // bright red for its welcome accent. Native terminal palettes render these as
  // warm orange tones, while xterm.js defaults make them look red/pink. The
  // lower status accent may arrive as bright magenta.
  1: "#d77757",
  9: "#d77757",
  13: "#f9b18f",
};

export const ANSI_256_OVERRIDES: Record<number, string> = {
  // Keep the 256-color aliases as well; Claude has used these for the same
  // accents in some startup/onboarding paths.
  174: "#d77757",
  211: "#f9b18f",
};

const extendedAnsi: string[] = [];
for (const [code, color] of Object.entries(ANSI_256_OVERRIDES)) {
  extendedAnsi[Number(code) - 16] = color;
}

export const TERMINAL_THEME: ITheme = {
  background: TERMINAL_BACKGROUND,
  foreground: TERMINAL_FOREGROUND,
  red: ANSI_STANDARD_OVERRIDES[1],
  brightRed: ANSI_STANDARD_OVERRIDES[9],
  brightMagenta: ANSI_STANDARD_OVERRIDES[13],
  extendedAnsi,
};
