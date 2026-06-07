import { test, expect } from "vitest";
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
  expect(clampAvatarCrop({ center_x: 0.02, center_y: 0.98, size: 0.5 })).toEqual({ center_x: 0.02, center_y: 0.98, size: 0.5 });
  // Out-of-image centers are pulled back onto the image.
  expect(clampAvatarCrop({ center_x: -0.3, center_y: 1.4, size: 0.5 })).toEqual({ center_x: 0, center_y: 1, size: 0.5 });
});

test("clampAvatarCrop lets the circle grow past the image up to the max size", () => {
  expect(clampAvatarCrop({ center_x: 0.5, center_y: 0.5, size: 2.5 })).toEqual({ center_x: 0.5, center_y: 0.5, size: 2.5 });
  expect(clampAvatarCrop({ center_x: 0.5, center_y: 0.5, size: 9 })).toEqual({ center_x: 0.5, center_y: 0.5, size: avatarCropMaxSize });
  expect(clampAvatarCrop({ center_x: 0.5, center_y: 0.5, size: 0.01 })).toEqual({ center_x: 0.5, center_y: 0.5, size: 0.12 });
});

test("cropToSourceRect maps an in-bounds crop to source pixels", () => {
  expect(cropToSourceRect({ center_x: 0.5, center_y: 0.5, size: 0.5 }, 1000, 800)).toEqual({ sx: 300, sy: 200, side: 400 });
});

test("cropToSourceRect lets the selection run past the source edges (transparent margin)", () => {
  // Center pinned to the top edge: half the circle hangs above the image, so
  // the source rect starts at a negative y. drawImage renders that band as
  // transparency.
  expect(cropToSourceRect({ center_x: 0.5, center_y: 0, size: 0.5 }, 400, 800)).toEqual({ sx: 100, sy: -100, side: 200 });
});

test("cropToSourceRect can request a selection larger than the whole image", () => {
  // size 2 on a square image => the entire image sits inside the circle with a
  // transparent ring around it. Origin is negative on both axes.
  expect(cropToSourceRect({ center_x: 0.5, center_y: 0.5, size: 2 }, 400, 400)).toEqual({ sx: -200, sy: -200, side: 800 });
});

test("avatarCropContainsPoint separates circle drags from background drags", () => {
  const crop = { center_x: 0.5, center_y: 0.5, size: 0.5 };

  expect(avatarCropContainsPoint(crop, 400, 800, 200, 450)).toBe(true);
  expect(avatarCropContainsPoint(crop, 400, 800, 200, 50)).toBe(false);
});

test("avatarCropContainsPoint has drag slop near the circle edge", () => {
  const crop = { center_x: 0.5, center_y: 0.5, size: 0.5 };

  expect(avatarCropContainsPoint(crop, 400, 800, 200, 506)).toBe(false);
  expect(avatarCropContainsPoint(crop, 400, 800, 200, 506, 12)).toBe(true);
});

test("avatar crop drag preserves the pointer offset when starting inside the circle", () => {
  const crop = { center_x: 0.5, center_y: 0.5, size: 0.5 };
  const offset = avatarCropDragOffset(crop, 400, 800, 200, 450);

  expect(offset).toEqual({ x: 0, y: 50 });
  expect(avatarCropFromImagePoint(crop, 400, 800, 200, 550, offset)).toEqual({ center_x: 0.5, center_y: 0.625, size: 0.5 });
});

test("avatar crop drag can latch and move the circle past the top edge", () => {
  const crop = { center_x: 0.5, center_y: 0.5, size: 0.5 };
  const offset = avatarCropDragOffset(crop, 400, 800, 200, 450);

  // Dragging the pointer above the image pins the center to the top edge; the
  // circle then overhangs the top into transparency.
  expect(avatarCropFromImagePoint(crop, 400, 800, 200, 40, offset)).toEqual({ center_x: 0.5, center_y: 0, size: 0.5 });
});

test("avatar crop drag recenters when starting outside the circle", () => {
  const crop = { center_x: 0.5, center_y: 0.5, size: 0.5 };
  const offset = avatarCropDragOffset(crop, 400, 800, 200, 50);

  expect(offset).toEqual({ x: 0, y: 0 });
  expect(avatarCropFromImagePoint(crop, 400, 800, 200, 50, offset)).toEqual({ center_x: 0.5, center_y: 0.0625, size: 0.5 });
});

test("resizeAvatarCrop adjusts by one crop control step and honors the bounds", () => {
  const crop = { center_x: 0.5, center_y: 0.5, size: 0.42 };

  expect(resizeAvatarCrop(crop, avatarCropControlStep)).toEqual({ center_x: 0.5, center_y: 0.5, size: 0.43 });
  expect(resizeAvatarCrop({ ...crop, size: 0.12 }, -avatarCropControlStep)).toEqual({ center_x: 0.5, center_y: 0.5, size: 0.12 });
  // Past the ceiling the size is clamped to the max instead of overshooting.
  expect(resizeAvatarCrop({ ...crop, size: 2.995 }, avatarCropControlStep)).toEqual({ center_x: 0.5, center_y: 0.5, size: avatarCropMaxSize });
});

test("nudgeAvatarCrop moves the crop center by one crop control step", () => {
  const crop = { center_x: 0.5, center_y: 0.5, size: 0.2 };

  expect(nudgeAvatarCrop(crop, avatarCropControlStep, -avatarCropControlStep)).toEqual({ center_x: 0.51, center_y: 0.49, size: 0.2 });
  // The center can now reach the very edge of the image (0) instead of being
  // held back by the circle radius.
  expect(nudgeAvatarCrop({ ...crop, center_x: 0.005 }, -avatarCropControlStep, 0)).toEqual({ center_x: 0, center_y: 0.5, size: 0.2 });
});
