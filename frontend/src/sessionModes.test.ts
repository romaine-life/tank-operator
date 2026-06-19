import { describe, expect, test } from "vitest";

import {
  RESTRICTED_GIT_CAPABILITY,
  hasRestrictedGit,
  interactionIconKind,
} from "./sessionModes";

describe("hasRestrictedGit", () => {
  test("detects the restricted-git capability in the durable list", () => {
    expect(hasRestrictedGit([RESTRICTED_GIT_CAPABILITY])).toBe(true);
    expect(hasRestrictedGit(["spirelens_mcp", RESTRICTED_GIT_CAPABILITY])).toBe(
      true,
    );
  });

  test("is false when the capability is absent", () => {
    expect(hasRestrictedGit([])).toBe(false);
    expect(hasRestrictedGit(["spirelens_mcp"])).toBe(false);
  });

  test("tolerates null/undefined capability arrays", () => {
    expect(hasRestrictedGit(null)).toBe(false);
    expect(hasRestrictedGit(undefined)).toBe(false);
  });

  test("matches the wire-format capability string exactly", () => {
    // Guards against drift from the backend constant
    // (SessionCapabilityRestrictedGit in backend-go/internal/sessionmodel).
    expect(RESTRICTED_GIT_CAPABILITY).toBe("restricted_git");
  });
});

describe("interactionIconKind", () => {
  test("keeps the monitor glyph for restricted GUI sessions (the default)", () => {
    expect(interactionIconKind("gui", true)).toBe("gui");
  });

  test("swaps in the git glyph for unrestricted GUI sessions (the opt-out)", () => {
    expect(interactionIconKind("gui", false)).toBe("unrestricted-git");
  });

  test("never swaps a cli session, regardless of the restricted flag", () => {
    // restricted_git is only granted to repo-backed GUI modes; a cli row must
    // keep its terminal glyph regardless so the indicator can't mislabel a
    // non-gui row.
    expect(interactionIconKind("cli", true)).toBe("cli");
    expect(interactionIconKind("cli", false)).toBe("cli");
  });
});
