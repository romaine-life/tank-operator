import { test, expect } from "vitest";

import { ADMIN_REFERENCE_LINKS } from "./adminReferenceLinks";

test("ADMIN_REFERENCE_LINKS exposes the curated session-config and policy docs in order", () => {
  expect(ADMIN_REFERENCE_LINKS.map((link) => link.id)).toEqual([
          "default-claude",
          "quality-timeframes",
          "migration-policy",
          "product-inspirations",
          "session-config",
          "developer-guide",
        ]);
});

test("every reference link is an absolute https URL on the tank-operator repo", () => {
  for (const link of ADMIN_REFERENCE_LINKS) {
    expect(link.href).toMatch(/^https:\/\/github\.com\/romaine-life\/tank-operator\//);
    // Throws if malformed; asserts the scheme explicitly.
    expect(new URL(link.href).protocol).toBe("https:");
  }
});

test("reference links have no duplicate ids or hrefs", () => {
  const ids = ADMIN_REFERENCE_LINKS.map((link) => link.id);
  const hrefs = ADMIN_REFERENCE_LINKS.map((link) => link.href);
  expect(new Set(ids).size).toBe(ids.length);
  expect(new Set(hrefs).size).toBe(hrefs.length);
});

test("every reference link has a non-empty label and description", () => {
  for (const link of ADMIN_REFERENCE_LINKS) {
    expect(link.label.trim().length > 0).toBeTruthy();
    expect(link.description.trim().length > 0).toBeTruthy();
  }
});

test("reference links point at the default session primer and the three policy docs", () => {
  const byId = Object.fromEntries(
    ADMIN_REFERENCE_LINKS.map((link) => [link.id, link.href]),
  );
  expect(byId["default-claude"].includes("k8s/session-config/default-claude.md")).toBeTruthy();
  expect(byId["quality-timeframes"].includes(
          "k8s/session-config/docs/quality-timeframes.md",
        )).toBeTruthy();
  expect(byId["migration-policy"].includes(
          "k8s/session-config/docs/migration-policy.md",
        )).toBeTruthy();
  expect(byId["product-inspirations"].includes(
          "k8s/session-config/docs/product-inspirations.md",
        )).toBeTruthy();
});
