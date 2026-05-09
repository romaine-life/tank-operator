export const ANSI_STANDARD_OVERRIDES: Record<number, string> = {
  // Claude Code's current startup screen emits standard red for the logo and
  // bright red for its welcome accent. Native terminal palettes render these as
  // warm orange tones. The lower status accent may arrive as bright magenta.
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
