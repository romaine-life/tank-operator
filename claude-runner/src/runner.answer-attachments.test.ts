import { test } from "node:test";
import assert from "node:assert/strict";
import { mkdtempSync, mkdirSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";

import { buildAnswerAttachmentBlocks } from "./runner.js";

// buildAnswerAttachmentBlocks is the screenshot-in-answer fix: it turns the
// input_reply command's attachments into AskUserQuestion tool-result content
// blocks. An image is read from the workspace and inlined as a base64 image
// block (pixels in context — the screenshot the user attached); a non-image
// becomes a path line; a missing/oversize/unreadable image becomes a visible
// note. None of these is a silent drop (the reported bug).

function tempWorkspace(): string {
  return mkdtempSync(path.join(tmpdir(), "tank-answer-attach-"));
}

test("inlines an image attachment as a base64 image content block", async () => {
  const root = tempWorkspace();
  try {
    mkdirSync(path.join(root, "screenshots"));
    const bytes = Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]); // PNG magic
    writeFileSync(path.join(root, "screenshots/3.png"), bytes);

    const blocks = await buildAnswerAttachmentBlocks(
      [
        {
          label: "Screenshot 1",
          name: "shot.png",
          kind: "image",
          path: "screenshots/3.png",
          abs_path: path.join(root, "screenshots/3.png"),
          size: bytes.byteLength,
        },
      ],
      root,
    );

    assert.equal(blocks.length, 1);
    const block = blocks[0] as { type: string; data?: string; mimeType?: string };
    assert.equal(block.type, "image");
    assert.equal(block.mimeType, "image/png");
    assert.equal(block.data, bytes.toString("base64"));
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test("emits a path line for a non-image attachment instead of inlining", async () => {
  const root = tempWorkspace();
  try {
    writeFileSync(path.join(root, "notes.txt"), "hello");
    const blocks = await buildAnswerAttachmentBlocks(
      [{ label: "notes.txt", name: "notes.txt", kind: "file", path: "notes.txt" }],
      root,
    );
    assert.equal(blocks.length, 1);
    const block = blocks[0] as { type: string; text?: string };
    assert.equal(block.type, "text");
    assert.match(block.text ?? "", /notes\.txt/);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test("emits a visible note (never a silent drop) for a missing image", async () => {
  const root = tempWorkspace();
  try {
    const blocks = await buildAnswerAttachmentBlocks(
      [{ label: "Screenshot 1", name: "gone.png", kind: "image", path: "screenshots/gone.png" }],
      root,
    );
    assert.equal(blocks.length, 1);
    const block = blocks[0] as { type: string; text?: string };
    assert.equal(block.type, "text");
    assert.match(block.text ?? "", /could not be (located|read)/);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test("rejects an abs_path that escapes the workspace root (no inline, visible note)", async () => {
  const root = tempWorkspace();
  try {
    const blocks = await buildAnswerAttachmentBlocks(
      [{ label: "escape", name: "passwd.png", kind: "image", abs_path: "/etc/passwd" }],
      root,
    );
    assert.equal(blocks.length, 1);
    assert.equal((blocks[0] as { type: string }).type, "text");
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test("returns no blocks for a text-only answer (no attachments)", async () => {
  assert.deepEqual(await buildAnswerAttachmentBlocks(undefined), []);
  assert.deepEqual(await buildAnswerAttachmentBlocks([]), []);
});
