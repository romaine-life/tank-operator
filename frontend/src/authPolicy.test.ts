import { test } from "node:test";
import assert from "node:assert/strict";

import { requiresGitHubOnboarding } from "./authPolicy";

test("standard users need GitHub onboarding until installation is present", () => {
  assert.equal(requiresGitHubOnboarding({ role: "user", installation_id: null }), true);
  assert.equal(requiresGitHubOnboarding({ role: "user", installation_id: 123 }), false);
});

test("admin and service callers bypass GitHub onboarding", () => {
  assert.equal(requiresGitHubOnboarding({ role: "admin", installation_id: null }), false);
  assert.equal(requiresGitHubOnboarding({ role: "service", installation_id: null }), false);
});
