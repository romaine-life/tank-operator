import assert from "node:assert/strict";
import test from "node:test";

import { ADMIN_REFERENCE_LINKS } from "./adminReferenceLinks";

test("ADMIN_REFERENCE_LINKS exposes the curated session-config and policy docs in order", () => {
  assert.deepEqual(
    ADMIN_REFERENCE_LINKS.map((link) => link.id),
    [
      "default-claude",
      "quality-timeframes",
      "migration-policy",
      "product-inspirations",
      "session-config",
      "developer-guide",
    ],
  );
});

test("every reference link is an absolute https URL on the tank-operator repo", () => {
  for (const link of ADMIN_REFERENCE_LINKS) {
    assert.match(
      link.href,
      /^https:\/\/github\.com\/nelsong6\/tank-operator\//,
    );
    // Throws if malformed; asserts the scheme explicitly.
    assert.equal(new URL(link.href).protocol, "https:");
  }
});

test("reference links have no duplicate ids or hrefs", () => {
  const ids = ADMIN_REFERENCE_LINKS.map((link) => link.id);
  const hrefs = ADMIN_REFERENCE_LINKS.map((link) => link.href);
  assert.equal(new Set(ids).size, ids.length);
  assert.equal(new Set(hrefs).size, hrefs.length);
});

test("every reference link has a non-empty label and description", () => {
  for (const link of ADMIN_REFERENCE_LINKS) {
    assert.ok(link.label.trim().length > 0);
    assert.ok(link.description.trim().length > 0);
  }
});

test("reference links point at the default session primer and the three policy docs", () => {
  const byId = Object.fromEntries(
    ADMIN_REFERENCE_LINKS.map((link) => [link.id, link.href]),
  );
  assert.ok(byId["default-claude"].includes("k8s/session-config/default-claude.md"));
  assert.ok(
    byId["quality-timeframes"].includes(
      "k8s/session-config/docs/quality-timeframes.md",
    ),
  );
  assert.ok(
    byId["migration-policy"].includes(
      "k8s/session-config/docs/migration-policy.md",
    ),
  );
  assert.ok(
    byId["product-inspirations"].includes(
      "k8s/session-config/docs/product-inspirations.md",
    ),
  );
});
