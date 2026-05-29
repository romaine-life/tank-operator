export function normalizeTranscriptCopyText(text: string): string {
  return text.replace(/[ \t\f\v]*(?:\r\n|\r|\n)\s*$/u, "");
}
