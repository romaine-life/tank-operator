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

function finiteOr(value: number | undefined, fallback: number): number {
  return typeof value === "number" && Number.isFinite(value) ? value : fallback;
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

function cropHalfExtents(size: number, width?: number, height?: number) {
  const dimensions = usableDimensions(width, height);
  if (!dimensions) {
    const half = size / 2;
    return { x: half, y: half };
  }
  const side = size * Math.min(dimensions.width, dimensions.height);
  return {
    x: Math.min(side / (2 * dimensions.width), 0.5),
    y: Math.min(side / (2 * dimensions.height), 0.5),
  };
}

export function clampAvatarCrop(
  crop: AvatarCrop,
  sourceWidth?: number,
  sourceHeight?: number,
): AvatarCrop {
  const size = Math.min(Math.max(finiteOr(crop.size, 0.5), 0.12), 1);
  const half = cropHalfExtents(size, sourceWidth, sourceHeight);
  const centerX = finiteOr(crop.center_x, 0.5);
  const centerY = finiteOr(crop.center_y, 0.5);
  return {
    ...crop,
    size,
    center_x: Math.min(Math.max(centerX, half.x), 1 - half.x),
    center_y: Math.min(Math.max(centerY, half.y), 1 - half.y),
  };
}

export function cropToSourceRect(
  crop: AvatarCrop,
  naturalWidth: number,
  naturalHeight: number,
): SourceCropRect {
  const normalized = clampAvatarCrop(crop, naturalWidth, naturalHeight);
  const side = normalized.size * Math.min(naturalWidth, naturalHeight);
  const centerX = normalized.center_x * naturalWidth;
  const centerY = normalized.center_y * naturalHeight;
  return {
    sx: Math.min(Math.max(centerX - side / 2, 0), naturalWidth - side),
    sy: Math.min(Math.max(centerY - side / 2, 0), naturalHeight - side),
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
  const normalized = clampAvatarCrop(crop, imageWidth, imageHeight);
  const side = normalized.size * Math.min(imageWidth, imageHeight);
  const centerX = normalized.center_x * imageWidth;
  const centerY = normalized.center_y * imageHeight;
  const x = pointerX - centerX;
  const y = pointerY - centerY;
  if (Math.hypot(x, y) <= side / 2 + tolerancePx) {
    return { x, y };
  }
  return { x: 0, y: 0 };
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
    return clampAvatarCrop(crop, imageWidth, imageHeight);
  }
  return clampAvatarCrop({
    ...crop,
    center_x: (pointerX - dragOffset.x) / imageWidth,
    center_y: (pointerY - dragOffset.y) / imageHeight,
  }, imageWidth, imageHeight);
}
