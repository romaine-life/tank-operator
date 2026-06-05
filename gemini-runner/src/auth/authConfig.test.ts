import assert from "node:assert/strict";
import {
  mkdirSync,
  mkdtempSync,
  readFileSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import { ensureGeminiSettingsFile } from "./authConfig.js";

test("ensureGeminiSettingsFile preserves mounted OAuth credentials", () => {
  const homeDir = mkdtempSync(join(tmpdir(), "tank-gemini-auth-"));
  try {
    const geminiDir = join(homeDir, ".gemini");
    mkdirSync(geminiDir, { recursive: true });
    const credsPath = join(geminiDir, "oauth_creds.json");
    const credentials = JSON.stringify({
      access_token: "real-access-token",
      refresh_token: "real-refresh-token",
      expiry_date: 1780248126223,
    });
    writeFileSync(credsPath, credentials, { mode: 0o600 });

    const settingsPath = ensureGeminiSettingsFile(homeDir);

    assert.equal(settingsPath, join(geminiDir, "settings.json"));
    assert.deepEqual(JSON.parse(readFileSync(settingsPath, "utf8")), {
      security: {
        auth: {
          selectedType: "oauth-personal",
        },
      },
    });
    assert.equal(readFileSync(credsPath, "utf8"), credentials);
  } finally {
    rmSync(homeDir, { recursive: true, force: true });
  }
});
