import type { ITheme } from "@xterm/xterm";

export const TERMINAL_BACKGROUND = "#171717";
export const TERMINAL_FOREGROUND = "#e4e4e4";

export const ANSI_256_OVERRIDES: Record<number, string> = {
  // Claude Code emits xterm-256 color 174 for its logo and welcome line.
  // xterm's stock 174 is pink; the native terminal palette Claude is tuned
  // against renders this as a muted orange.
  174: "#d77757",
  // Same Claude accent family for the secondary status line.
  211: "#f9b18f",
};

const extendedAnsi: string[] = [];
for (const [code, color] of Object.entries(ANSI_256_OVERRIDES)) {
  extendedAnsi[Number(code) - 16] = color;
}

export const TERMINAL_THEME: ITheme = {
  background: TERMINAL_BACKGROUND,
  foreground: TERMINAL_FOREGROUND,
  extendedAnsi,
};
