import { mkdirSync, writeFileSync } from "node:fs";
import { homedir } from "node:os";
import { join } from "node:path";

export function ensureGeminiSettingsFile(homeDir = homedir()): string {
  const geminiDir = join(homeDir, ".gemini");
  mkdirSync(geminiDir, { recursive: true });

  const settingsPath = join(geminiDir, "settings.json");
  writeFileSync(
    settingsPath,
    JSON.stringify(
      {
        security: {
          auth: {
            selectedType: "oauth-personal",
          },
        },
      },
      null,
      2
    ),
    { mode: 0o600 }
  );
  return settingsPath;
}
