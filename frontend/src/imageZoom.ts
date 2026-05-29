/**
 * Pure zoom math for the workspace image/screenshot viewer.
 *
 * Kept dependency-free (no React, no DOM) so the zoom behaviour can be unit
 * tested in isolation and reused by the `FileImageViewer` component. The
 * component owns the imperative bits (refs, wheel/pointer handlers); the rules
 * for "what scale comes next" live here.
 */

/** Smallest zoom factor (10% of natural pixel size). */
export const MIN_SCALE = 0.1;
/** Largest zoom factor (800% of natural pixel size). */
export const MAX_SCALE = 8;
/** Multiplicative step applied by the +/- zoom buttons. */
export const ZOOM_STEP = 1.25;
/** Multiplicative step applied by one normal mouse-wheel notch. */
export const WHEEL_ZOOM_STEP = 1.1;

const WHEEL_DELTA_PIXELS_PER_STEP = 100;
const WHEEL_LINE_HEIGHT_PX = 40;
const WHEEL_PAGE_HEIGHT_PX = 800;
const MAX_WHEEL_STEPS_PER_EVENT = 6;

export interface Size {
  width: number;
  height: number;
}

/** Clamp an arbitrary scale into the supported [MIN_SCALE, MAX_SCALE] range. */
export function clampScale(scale: number): number {
  if (!Number.isFinite(scale)) return 1;
  return Math.min(MAX_SCALE, Math.max(MIN_SCALE, scale));
}

/**
 * Scale that makes the natural image fit fully inside the container while
 * preserving aspect ratio. Capped at 1 so small images are shown at their
 * native pixel size rather than being upscaled (matching the prior
 * `object-fit: contain` + `max-*: 100%` behaviour). Returns 1 when either
 * dimension is unknown.
 */
export function computeFitScale(natural: Size, container: Size): number {
  if (
    natural.width <= 0 ||
    natural.height <= 0 ||
    container.width <= 0 ||
    container.height <= 0
  ) {
    return 1;
  }
  const fit = Math.min(
    container.width / natural.width,
    container.height / natural.height,
  );
  return Math.min(1, fit);
}

/** Next scale when zooming in by one button step. */
export function zoomIn(current: number): number {
  return clampScale(current * ZOOM_STEP);
}

/** Next scale when zooming out by one button step. */
export function zoomOut(current: number): number {
  return clampScale(current / ZOOM_STEP);
}

/** Apply an arbitrary multiplicative factor (used for wheel zoom). */
export function zoomBy(current: number, factor: number): number {
  return clampScale(current * factor);
}

/**
 * Convert a DOM wheel delta into a multiplicative zoom factor. Negative
 * `deltaY` zooms in, positive zooms out. Small high-resolution trackpad
 * deltas produce proportionally smaller zoom changes than a full wheel notch.
 */
export function wheelZoomFactor(deltaY: number, deltaMode = 0): number {
  if (!Number.isFinite(deltaY) || deltaY === 0) return 1;
  const unit =
    deltaMode === 1
      ? WHEEL_LINE_HEIGHT_PX
      : deltaMode === 2
        ? WHEEL_PAGE_HEIGHT_PX
        : 1;
  const steps = Math.max(
    -MAX_WHEEL_STEPS_PER_EVENT,
    Math.min(MAX_WHEEL_STEPS_PER_EVENT, (deltaY * unit) / WHEEL_DELTA_PIXELS_PER_STEP),
  );
  return Math.pow(WHEEL_ZOOM_STEP, -steps);
}

/** Whole-number percentage for the zoom indicator (e.g. 1.25 -> 125). */
export function formatZoomPercent(scale: number): number {
  return Math.round(scale * 100);
}

/**
 * Whether two scales are close enough to treat as equal. Used to decide when a
 * zoomed view has returned to the fit baseline so we can drop back into
 * fit-mode (and re-enable responsive resizing).
 */
export function scalesEqual(a: number, b: number, epsilon = 0.001): boolean {
  return Math.abs(a - b) <= epsilon;
}
