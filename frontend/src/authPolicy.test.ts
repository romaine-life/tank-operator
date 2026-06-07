import { test, expect } from "vitest";
import { requiresGitHubOnboarding } from "./authPolicy";

test("standard users need GitHub onboarding until installation is present", () => {
  expect(requiresGitHubOnboarding({ role: "user", installation_id: null })).toBe(true);
  expect(requiresGitHubOnboarding({ role: "user", installation_id: 123 })).toBe(false);
});

test("admin and service callers bypass GitHub onboarding", () => {
  expect(requiresGitHubOnboarding({ role: "admin", installation_id: null })).toBe(false);
  expect(requiresGitHubOnboarding({ role: "service", installation_id: null })).toBe(false);
});
