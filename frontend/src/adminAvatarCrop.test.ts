import assert from "node:assert/strict";
import test from "node:test";
import {
  avatarCropControlStep,
  avatarCropDragOffset,
  avatarCropContainsPoint,
  avatarCropFromImagePoint,
  avatarCropMaxSize,
  clampAvatarCrop,
  cropToSourceRect,
  nudgeAvatarCrop,
  resizeAvatarCrop,
} from "./adminAvatarCrop";

test("clampAvatarCrop keeps the selection center on the image but lets the circle overhang", () => {
  // Center stays on the image; the circle is free to hang past the edges.
  assert.deepEqual(
    clampAvatarCrop({ center_x: 0.02, center_y: 0.98, size: 0.5 }),
    { center_x: 0.02, center_y: 0.98, size: 0.5 },
  );
  // Out-of-image centers are pulled back onto the image.
  assert.deepEqual(
    clampAvatarCrop({ center_x: -0.3, center_y: 1.4, size: 0.5 }),
    { center_x: 0, center_y: 1, size: 0.5 },
  );
});

test("clampAvatarCrop lets the circle grow past the image up to the max size", () => {
  assert.deepEqual(
    clampAvatarCrop({ center_x: 0.5, center_y: 0.5, size: 2.5 }),
    { center_x: 0.5, center_y: 0.5, size: 2.5 },
  );
  assert.deepEqual(
    clampAvatarCrop({ center_x: 0.5, center_y: 0.5, size: 9 }),
    { center_x: 0.5, center_y: 0.5, size: avatarCropMaxSize },
  );
  assert.deepEqual(
    clampAvatarCrop({ center_x: 0.5, center_y: 0.5, size: 0.01 }),
    { center_x: 0.5, center_y: 0.5, size: 0.12 },
  );
});

test("cropToSourceRect maps an in-bounds crop to source pixels", () => {
  assert.deepEqual(
    cropToSourceRect({ center_x: 0.5, center_y: 0.5, size: 0.5 }, 1000, 800),
    { sx: 300, sy: 200, side: 400 },
  );
});

test("cropToSourceRect lets the selection run past the source edges (transparent margin)", () => {
  // Center pinned to the top edge: half the circle hangs above the image, so
  // the source rect starts at a negative y. drawImage renders that band as
  // transparency.
  assert.deepEqual(
    cropToSourceRect({ center_x: 0.5, center_y: 0, size: 0.5 }, 400, 800),
    { sx: 100, sy: -100, side: 200 },
  );
});

test("cropToSourceRect can request a selection larger than the whole image", () => {
  // size 2 on a square image => the entire image sits inside the circle with a
  // transparent ring around it. Origin is negative on both axes.
  assert.deepEqual(
    cropToSourceRect({ center_x: 0.5, center_y: 0.5, size: 2 }, 400, 400),
    { sx: -200, sy: -200, side: 800 },
  );
});

test("avatarCropContainsPoint separates circle drags from background drags", () => {
  const crop = { center_x: 0.5, center_y: 0.5, size: 0.5 };

  assert.equal(avatarCropContainsPoint(crop, 400, 800, 200, 450), true);
  assert.equal(avatarCropContainsPoint(crop, 400, 800, 200, 50), false);
});

test("avatarCropContainsPoint has drag slop near the circle edge", () => {
  const crop = { center_x: 0.5, center_y: 0.5, size: 0.5 };

  assert.equal(avatarCropContainsPoint(crop, 400, 800, 200, 506), false);
  assert.equal(avatarCropContainsPoint(crop, 400, 800, 200, 506, 12), true);
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

test("avatar crop drag can latch and move the circle past the top edge", () => {
  const crop = { center_x: 0.5, center_y: 0.5, size: 0.5 };
  const offset = avatarCropDragOffset(crop, 400, 800, 200, 450);

  // Dragging the pointer above the image pins the center to the top edge; the
  // circle then overhangs the top into transparency.
  assert.deepEqual(
    avatarCropFromImagePoint(crop, 400, 800, 200, 40, offset),
    { center_x: 0.5, center_y: 0, size: 0.5 },
  );
});

test("avatar crop drag recenters when starting outside the circle", () => {
  const crop = { center_x: 0.5, center_y: 0.5, size: 0.5 };
  const offset = avatarCropDragOffset(crop, 400, 800, 200, 50);

  assert.deepEqual(offset, { x: 0, y: 0 });
  assert.deepEqual(
    avatarCropFromImagePoint(crop, 400, 800, 200, 50, offset),
    { center_x: 0.5, center_y: 0.0625, size: 0.5 },
  );
});

test("resizeAvatarCrop adjusts by one crop control step and honors the bounds", () => {
  const crop = { center_x: 0.5, center_y: 0.5, size: 0.42 };

  assert.deepEqual(
    resizeAvatarCrop(crop, avatarCropControlStep),
    { center_x: 0.5, center_y: 0.5, size: 0.43 },
  );
  assert.deepEqual(
    resizeAvatarCrop({ ...crop, size: 0.12 }, -avatarCropControlStep),
    { center_x: 0.5, center_y: 0.5, size: 0.12 },
  );
  // Past the ceiling the size is clamped to the max instead of overshooting.
  assert.deepEqual(
    resizeAvatarCrop({ ...crop, size: 2.995 }, avatarCropControlStep),
    { center_x: 0.5, center_y: 0.5, size: avatarCropMaxSize },
  );
});

test("nudgeAvatarCrop moves the crop center by one crop control step", () => {
  const crop = { center_x: 0.5, center_y: 0.5, size: 0.2 };

  assert.deepEqual(
    nudgeAvatarCrop(crop, avatarCropControlStep, -avatarCropControlStep),
    { center_x: 0.51, center_y: 0.49, size: 0.2 },
  );
  // The center can now reach the very edge of the image (0) instead of being
  // held back by the circle radius.
  assert.deepEqual(
    nudgeAvatarCrop({ ...crop, center_x: 0.005 }, -avatarCropControlStep, 0),
    { center_x: 0, center_y: 0.5, size: 0.2 },
  );
});
