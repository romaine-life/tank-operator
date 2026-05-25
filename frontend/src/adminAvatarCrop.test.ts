import assert from "node:assert/strict";
import test from "node:test";
import { clampAvatarCrop, cropToSourceRect } from "./adminAvatarCrop";

test("clampAvatarCrop keeps the circular selection inside the image", () => {
  assert.deepEqual(
    clampAvatarCrop({ center_x: 0.02, center_y: 0.98, size: 0.5 }),
    { center_x: 0.25, center_y: 0.75, size: 0.5 },
  );
});

test("cropToSourceRect maps normalized crop to source pixels", () => {
  assert.deepEqual(
    cropToSourceRect({ center_x: 0.5, center_y: 0.5, size: 0.5 }, 1000, 800),
    { sx: 300, sy: 200, side: 400 },
  );
});
