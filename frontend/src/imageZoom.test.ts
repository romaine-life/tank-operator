import { test, expect } from "vitest";

import {
  MAX_SCALE,
  MIN_SCALE,
  ZOOM_STEP,
  WHEEL_ZOOM_STEP,
  clampScale,
  computeFitScale,
  formatZoomPercent,
  scalesEqual,
  wheelZoomFactor,
  zoomBy,
  zoomIn,
  zoomOut,
} from "./imageZoom";

test("clampScale keeps values within the supported range", () => {
  expect(clampScale(1)).toBe(1);
  expect(clampScale(100)).toBe(MAX_SCALE);
  expect(clampScale(0)).toBe(MIN_SCALE);
  expect(clampScale(-5)).toBe(MIN_SCALE);
});

test("clampScale falls back to 1 for non-finite input", () => {
  expect(clampScale(Number.NaN)).toBe(1);
  expect(clampScale(Number.POSITIVE_INFINITY)).toBe(1);
  expect(clampScale(Number.NEGATIVE_INFINITY)).toBe(1);
});

test("computeFitScale shrinks large images to fit the container", () => {
  // 2000x1000 image inside a 1000x1000 box -> limited by width -> 0.5.
  const fit = computeFitScale(
    { width: 2000, height: 1000 },
    { width: 1000, height: 1000 },
  );
  expect(fit).toBe(0.5);
});

test("computeFitScale never upscales small images past natural size", () => {
  const fit = computeFitScale(
    { width: 100, height: 100 },
    { width: 1000, height: 1000 },
  );
  expect(fit).toBe(1);
});

test("computeFitScale is defensive about unknown dimensions", () => {
  expect(computeFitScale({ width: 0, height: 0 }, { width: 10, height: 10 })).toBe(1);
  expect(computeFitScale({ width: 10, height: 10 }, { width: 0, height: 0 })).toBe(1);
});

test("zoomIn and zoomOut step by the configured factor and round-trip", () => {
  expect(zoomIn(1)).toBe(ZOOM_STEP);
  expect(zoomOut(1)).toBe(1 / ZOOM_STEP);
  expect(scalesEqual(zoomOut(zoomIn(1)), 1)).toBeTruthy();
});

test("zoom steps saturate at the range bounds", () => {
  expect(zoomIn(MAX_SCALE)).toBe(MAX_SCALE);
  expect(zoomOut(MIN_SCALE)).toBe(MIN_SCALE);
});

test("zoomBy applies an arbitrary factor with clamping", () => {
  expect(scalesEqual(zoomBy(1, 2), 2)).toBeTruthy();
  expect(zoomBy(1, 1000)).toBe(MAX_SCALE);
});

test("wheelZoomFactor maps mouse wheel direction to zoom direction", () => {
  expect(scalesEqual(wheelZoomFactor(-100), WHEEL_ZOOM_STEP)).toBeTruthy();
  expect(scalesEqual(wheelZoomFactor(100), 1 / WHEEL_ZOOM_STEP)).toBeTruthy();
});

test("wheelZoomFactor scales high-resolution and line-mode deltas", () => {
  expect(wheelZoomFactor(-10) > 1).toBeTruthy();
  expect(wheelZoomFactor(-10) < WHEEL_ZOOM_STEP).toBeTruthy();
  expect(scalesEqual(wheelZoomFactor(-3, 1), Math.pow(WHEEL_ZOOM_STEP, 1.2))).toBeTruthy();
});

test("wheelZoomFactor ignores empty or non-finite deltas", () => {
  expect(wheelZoomFactor(0)).toBe(1);
  expect(wheelZoomFactor(Number.NaN)).toBe(1);
});

test("formatZoomPercent renders whole-number percentages", () => {
  expect(formatZoomPercent(1)).toBe(100);
  expect(formatZoomPercent(1.25)).toBe(125);
  expect(formatZoomPercent(0.333)).toBe(33);
});
