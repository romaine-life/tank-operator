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

export function clampAvatarCrop(crop: AvatarCrop): AvatarCrop {
  const size = Math.min(Math.max(crop.size || 0.5, 0.12), 1);
  const half = size / 2;
  return {
    ...crop,
    size,
    center_x: Math.min(Math.max(crop.center_x, half), 1 - half),
    center_y: Math.min(Math.max(crop.center_y, half), 1 - half),
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
  return {
    sx: Math.min(Math.max(centerX - side / 2, 0), naturalWidth - side),
    sy: Math.min(Math.max(centerY - side / 2, 0), naturalHeight - side),
    side,
  };
}
