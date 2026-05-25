import assert from "node:assert/strict";
import test from "node:test";
import {
  avatarCropDragOffset,
  avatarCropFromImagePoint,
  clampAvatarCrop,
  cropToSourceRect,
} from "./adminAvatarCrop";

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

test("clampAvatarCrop uses image aspect ratio so tall images can crop at the top edge", () => {
  assert.deepEqual(
    clampAvatarCrop({ center_x: 0.5, center_y: 0, size: 0.5 }, 400, 800),
    { center_x: 0.5, center_y: 0.125, size: 0.5 },
  );
});

test("cropToSourceRect can place a tall-image crop at the top source edge", () => {
  assert.deepEqual(
    cropToSourceRect({ center_x: 0.5, center_y: 0, size: 0.5 }, 400, 800),
    { sx: 100, sy: 0, side: 200 },
  );
});

test("avatar crop drag preserves the pointer offset when starting inside the circle", () => {
  const crop = { center_x: 0.5, center_y: 0.5, size: 0.5 };
  const offset = avatarCropDragOffset(crop, 400, 800, 200, 450);

  assert.deepEqual(offset, { x: 0, y: 50 });
  assert.deepEqual(
    avatarCropFromImagePoint(crop, 400, 800, 200, 550, offset),
    { center_x: 0.5, center_y: 0.625, size: 0.5 },
  );
});

test("avatar crop drag recenters when starting outside the circle", () => {
  const crop = { center_x: 0.5, center_y: 0.5, size: 0.5 };
  const offset = avatarCropDragOffset(crop, 400, 800, 200, 50);

  assert.deepEqual(offset, { x: 0, y: 0 });
  assert.deepEqual(
    avatarCropFromImagePoint(crop, 400, 800, 200, 50, offset),
    { center_x: 0.5, center_y: 0.125, size: 0.5 },
  );
});
