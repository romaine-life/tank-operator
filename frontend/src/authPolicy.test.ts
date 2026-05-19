import { test } from "node:test";
import assert from "node:assert/strict";

import { requiresGitHubOnboarding } from "./authPolicy";

test("standard users need GitHub onboarding until installation is present", () => {
  assert.equal(
    requiresGitHubOnboarding({
      role: "user",
      installation_id: null,
      github_access: {
        repo_source: "none",
        can_list_repos: false,
        requires_onboarding: true,
      },
    }),
    true,
  );
  assert.equal(
    requiresGitHubOnboarding({
      role: "user",
      installation_id: 123,
      github_access: {
        repo_source: "user_installation",
        can_list_repos: true,
        requires_onboarding: false,
      },
    }),
    false,
  );
});

test("onboarding follows server github access policy", () => {
  assert.equal(
    requiresGitHubOnboarding({
      role: "admin",
      installation_id: null,
      github_access: {
        repo_source: "host_installation",
        can_list_repos: true,
        requires_onboarding: false,
      },
    }),
    false,
  );
  assert.equal(
    requiresGitHubOnboarding({
      role: "service",
      installation_id: null,
      github_access: {
        repo_source: "none",
        can_list_repos: false,
        requires_onboarding: false,
      },
    }),
    false,
  );
});
