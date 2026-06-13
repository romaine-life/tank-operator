import { expect, test } from "vitest";
import {
  DENY_PREFIXES,
  READABLE_ROOTS,
  homeTildeDisplay,
  isDeniedPath,
  joinDir,
  parentDir,
} from "./workspaceRoots";

test("READABLE_ROOTS are the agent-relevant bookmark dirs", () => {
  expect(READABLE_ROOTS.map((r) => r.path)).toEqual([
    "/workspace",
    "/home/node",
    "/opt/tank",
    "/tmp",
  ]);
});

test("isDeniedPath fences the secret token mounts and their realpath form", () => {
  // /var/run -> /run on the Debian session image, so both forms are denied.
  expect(isDeniedPath("/var/run/secrets/auth.romaine.life/token")).toBe(true);
  expect(isDeniedPath("/run/secrets/auth.romaine.life/token")).toBe(true);
  expect(isDeniedPath("/var/run/secrets")).toBe(true);
  expect(isDeniedPath("/proc/1/environ")).toBe(true);
  expect(isDeniedPath("/sys/kernel")).toBe(true);
  expect(DENY_PREFIXES).toContain("/run/secrets/");
  // Not secrets — readable (default-allow).
  expect(isDeniedPath("/workspace/src/App.tsx")).toBe(false);
  expect(isDeniedPath("/home/node/.claude/plan.md")).toBe(false);
  expect(isDeniedPath("/var/run/secretsfoo")).toBe(false);
});

test("joinDir builds absolute child paths", () => {
  expect(joinDir("/workspace", "src")).toBe("/workspace/src");
  expect(joinDir("/home/node/.claude", "plan.md")).toBe(
    "/home/node/.claude/plan.md",
  );
  expect(joinDir("/", "etc")).toBe("/etc");
});

test("parentDir walks up and clamps at the filesystem root", () => {
  expect(parentDir("/workspace/src/App.tsx")).toBe("/workspace/src");
  expect(parentDir("/workspace")).toBe("/");
  expect(parentDir("/home/node/.claude")).toBe("/home/node");
  expect(parentDir("/")).toBe("/");
  expect(parentDir("")).toBe("/");
});

test("homeTildeDisplay renders the agent home compactly", () => {
  expect(homeTildeDisplay("/home/node")).toBe("~");
  expect(homeTildeDisplay("/home/node/.claude/plan.md")).toBe(
    "~/.claude/plan.md",
  );
  expect(homeTildeDisplay("/workspace/x")).toBe("/workspace/x");
});
