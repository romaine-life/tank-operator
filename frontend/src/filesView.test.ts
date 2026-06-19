import { expect, test } from "vitest";

import { filesBodyClassName, isFilesDetailMode } from "./filesView.ts";

// The master-detail rule: phone + a selected file -> viewer (detail) mode;
// everything else keeps the list / the desktop two-pane layout. The rendered
// swap and the in-viewer back affordance are validated end-to-end (deep-linked
// files routes against a live slot); this pins the decision itself.

test("phone + a selected file enters detail (viewer) mode", () => {
  expect(isFilesDetailMode(true, true)).toBe(true);
  expect(filesBodyClassName(true, true)).toBe(
    "run-files-body run-files-body-detail",
  );
});

test("phone with no file selected stays on the list", () => {
  expect(isFilesDetailMode(true, false)).toBe(false);
  expect(filesBodyClassName(true, false)).toBe("run-files-body");
});

test("desktop keeps the two-pane layout even with a file selected", () => {
  expect(isFilesDetailMode(false, true)).toBe(false);
  expect(filesBodyClassName(false, true)).toBe("run-files-body");
});
