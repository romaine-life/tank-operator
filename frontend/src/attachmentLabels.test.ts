import assert from "node:assert/strict";
import { test } from "node:test";

import {
  composeAttachmentDisplayText,
  composeAttachmentPathText,
  labelAttachments,
} from "./attachmentLabels.ts";

test("labels repeated generic clipboard images as screenshots", () => {
  assert.deepEqual(
    labelAttachments([
      { name: "image.png", type: "image/png" },
      { name: "image.png", type: "image/png" },
    ]).map((item) => item.label),
    ["Screenshot 1", "Screenshot 2"],
  );
});

test("continues screenshot numbering after existing attachments", () => {
  assert.deepEqual(
    labelAttachments(
      [{ name: "image.png", type: "image/png" }],
      [{ name: "image.png", label: "Screenshot 1" }],
    ).map((item) => item.label),
    ["Screenshot 2"],
  );
});

test("preserves meaningful filenames and disambiguates duplicate names", () => {
  assert.deepEqual(
    labelAttachments([
      { name: "flow.png", type: "image/png" },
      { name: "flow.png", type: "image/png" },
      { name: "notes.txt", type: "text/plain" },
      { name: "notes.txt", type: "text/plain" },
    ]).map((item) => item.label),
    ["flow.png", "flow 2.png", "notes.txt", "notes 2.txt"],
  );
});

test("uses attachment labels for generic non-image files", () => {
  assert.deepEqual(
    labelAttachments([
      { name: "file", type: "application/octet-stream" },
      { name: "", type: "application/octet-stream" },
    ]).map((item) => item.label),
    ["Attachment 1", "Attachment 2"],
  );
});

test("composes user-visible attachment text from labels", () => {
  assert.equal(
    composeAttachmentDisplayText("please compare these", [
      { name: "image.png", label: "Screenshot 1" },
      { name: "image.png", label: "Screenshot 2" },
    ]),
    "please compare these\n\nAttachments:\n- Screenshot 1\n- Screenshot 2",
  );
});

test("composes runner attachment text from paths without tool instructions", () => {
  assert.equal(
    composeAttachmentPathText("please compare these", [
      "/workspace/screenshots/1.png",
      "/workspace/screenshots/2.png",
    ]),
    "please compare these\n\nAttachments:\n- /workspace/screenshots/1.png\n- /workspace/screenshots/2.png",
  );
});
