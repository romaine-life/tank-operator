import { test, expect } from "vitest";

import {
  composeAttachmentDisplayText,
  composeAttachmentPathText,
  labelAttachments,
  messageAttachmentDisplays,
  splitLegacyAttachmentDisplayText,
} from "./attachmentLabels.ts";

test("labels repeated generic clipboard images as screenshots", () => {
  expect(labelAttachments([
          { name: "image.png", type: "image/png" },
          { name: "image.png", type: "image/png" },
        ]).map((item) => item.label)).toEqual(["Screenshot 1", "Screenshot 2"]);
});

test("continues screenshot numbering after existing attachments", () => {
  expect(labelAttachments(
          [{ name: "image.png", type: "image/png" }],
          [{ name: "image.png", label: "Screenshot 1" }],
        ).map((item) => item.label)).toEqual(["Screenshot 2"]);
});

test("preserves meaningful filenames and disambiguates duplicate names", () => {
  expect(labelAttachments([
          { name: "flow.png", type: "image/png" },
          { name: "flow.png", type: "image/png" },
          { name: "notes.txt", type: "text/plain" },
          { name: "notes.txt", type: "text/plain" },
        ]).map((item) => item.label)).toEqual(["flow.png", "flow 2.png", "notes.txt", "notes 2.txt"]);
});

test("uses attachment labels for generic non-image files", () => {
  expect(labelAttachments([
          { name: "file", type: "application/octet-stream" },
          { name: "", type: "application/octet-stream" },
        ]).map((item) => item.label)).toEqual(["Attachment 1", "Attachment 2"]);
});

test("keeps user-visible attachment text focused on the typed message", () => {
  expect(composeAttachmentDisplayText("please compare these", [
          { name: "image.png", label: "Screenshot 1" },
          { name: "image.png", label: "Screenshot 2" },
        ])).toBe("please compare these");
});

test("uses a neutral fallback for attachment-only display text", () => {
  expect(composeAttachmentDisplayText("", [
          { name: "image.png", label: "Screenshot 1" },
          { name: "image.png", label: "Screenshot 2" },
        ])).toBe("Attached 2 files");
});

test("composes runner attachment text from paths without tool instructions", () => {
  expect(composeAttachmentPathText("please compare these", [
          "/workspace/screenshots/1.png",
          "/workspace/screenshots/2.png",
        ])).toBe("please compare these\n\nAttachments:\n- /workspace/screenshots/1.png\n- /workspace/screenshots/2.png");
});

test("builds structured message attachment displays", () => {
  expect(messageAttachmentDisplays([
          {
            name: "image.png",
            label: "Screenshot 1",
            absPath: "/workspace/screenshots/1.png",
            path: "screenshots/1.png",
            size: 42,
          },
        ])).toEqual([
          {
            name: "image.png",
            label: "Screenshot 1",
            kind: "image",
            absPath: "/workspace/screenshots/1.png",
            path: "screenshots/1.png",
            size: 42,
          },
        ]);
});

test("splits legacy attachment display text into body plus attachments", () => {
  expect(splitLegacyAttachmentDisplayText(
          "please compare these\n\nAttachments:\n- /workspace/screenshots/1.png\n- Screenshot 2",
        )).toEqual({
          text: "please compare these",
          attachments: [
            {
              label: "Screenshot 1",
              name: "1.png",
              kind: "image",
              path: "screenshots/1.png",
              absPath: "/workspace/screenshots/1.png",
            },
            {
              label: "Screenshot 2",
              name: "Screenshot 2",
              kind: "file",
            },
          ],
        });
});
