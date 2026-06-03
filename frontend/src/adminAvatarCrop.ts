export type AvatarCrop = {
  center_x: number;
  center_y: number;
  size: number;
  source_width?: number;
  source_height?: number;
};

export type SourceCropRect = {
  sx: number;
  sy: number;
  side: number;
};

export type AvatarCropDragOffset = {
  x: number;
  y: number;
};

export const avatarCropControlStep = 0.01;

// The circular selection may be shrunk to a thumbnail or grown well past the
// edges of the source image. Whatever the circle covers outside the image is
// rendered as transparency by the canvas crop, so the only real limits are a
// small floor (keep the crop usable) and a generous ceiling (let an entire
// off-square image sit inside the avatar with transparent padding). The ceiling
// is mirrored by the backend `maxAvatarCropSize` sanity check in
// handlers_avatars.go — keep the two in lockstep.
export const avatarCropMinSize = 0.12;
export const avatarCropMaxSize = 3;

function finiteOr(value: number | undefined, fallback: number): number {
  return typeof value === "number" && Number.isFinite(value) ? value : fallback;
}

function clamp(value: number, min: number, max: number): number {
  return Math.min(Math.max(value, min), max);
}

function steppedCropValue(value: number): number {
  return Number(value.toFixed(4));
}

function usableDimensions(width?: number, height?: number): { width: number; height: number } | null {
  if (
    typeof width === "number" &&
    typeof height === "number" &&
    Number.isFinite(width) &&
    Number.isFinite(height) &&
    width > 0 &&
    height > 0
  ) {
    return { width, height };
  }
  return null;
}

export function clampAvatarCrop(crop: AvatarCrop): AvatarCrop {
  return {
    ...crop,
    size: clamp(finiteOr(crop.size, 0.5), avatarCropMinSize, avatarCropMaxSize),
    // The center stays on the image so the selection always covers real pixels,
    // but the circle itself is free to overhang any edge into transparency.
    center_x: clamp(finiteOr(crop.center_x, 0.5), 0, 1),
    center_y: clamp(finiteOr(crop.center_y, 0.5), 0, 1),
  };
}

export function cropToSourceRect(
  crop: AvatarCrop,
  naturalWidth: number,
  naturalHeight: number,
): SourceCropRect {
  const normalized = clampAvatarCrop(crop);
  const side = normalized.size * Math.min(naturalWidth, naturalHeight);
  const centerX = normalized.center_x * naturalWidth;
  const centerY = normalized.center_y * naturalHeight;
  // Intentionally unclamped: a selection that runs past the image edges keeps
  // its true (possibly negative origin / larger-than-image) source rect.
  // drawImage paints only the pixels that overlap the image and leaves the rest
  // of the avatar transparent.
  return {
    sx: centerX - side / 2,
    sy: centerY - side / 2,
    side,
  };
}

export function avatarCropDragOffset(
  crop: AvatarCrop,
  imageWidth: number,
  imageHeight: number,
  pointerX: number,
  pointerY: number,
  tolerancePx = 10,
): AvatarCropDragOffset {
  if (!usableDimensions(imageWidth, imageHeight)) return { x: 0, y: 0 };
  const normalized = clampAvatarCrop(crop);
  const centerX = normalized.center_x * imageWidth;
  const centerY = normalized.center_y * imageHeight;
  const x = pointerX - centerX;
  const y = pointerY - centerY;
  if (avatarCropContainsPoint(crop, imageWidth, imageHeight, pointerX, pointerY, tolerancePx)) {
    return { x, y };
  }
  return { x: 0, y: 0 };
}

export function avatarCropContainsPoint(
  crop: AvatarCrop,
  imageWidth: number,
  imageHeight: number,
  pointerX: number,
  pointerY: number,
  tolerancePx = 0,
): boolean {
  if (!usableDimensions(imageWidth, imageHeight)) return false;
  const normalized = clampAvatarCrop(crop);
  const side = normalized.size * Math.min(imageWidth, imageHeight);
  const centerX = normalized.center_x * imageWidth;
  const centerY = normalized.center_y * imageHeight;
  return Math.hypot(pointerX - centerX, pointerY - centerY) <= side / 2 + tolerancePx;
}

export function avatarCropFromImagePoint(
  crop: AvatarCrop,
  imageWidth: number,
  imageHeight: number,
  pointerX: number,
  pointerY: number,
  dragOffset: AvatarCropDragOffset = { x: 0, y: 0 },
): AvatarCrop {
  if (!usableDimensions(imageWidth, imageHeight)) {
    return clampAvatarCrop(crop);
  }
  return clampAvatarCrop({
    ...crop,
    center_x: (pointerX - dragOffset.x) / imageWidth,
    center_y: (pointerY - dragOffset.y) / imageHeight,
  });
}

export function resizeAvatarCrop(crop: AvatarCrop, deltaSize: number): AvatarCrop {
  return clampAvatarCrop({
    ...crop,
    size: steppedCropValue(finiteOr(crop.size, 0.5) + deltaSize),
  });
}

export function nudgeAvatarCrop(crop: AvatarCrop, deltaX: number, deltaY: number): AvatarCrop {
  return clampAvatarCrop({
    ...crop,
    center_x: steppedCropValue(finiteOr(crop.center_x, 0.5) + deltaX),
    center_y: steppedCropValue(finiteOr(crop.center_y, 0.5) + deltaY),
  });
}
