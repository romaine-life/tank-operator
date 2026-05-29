import assert from "node:assert/strict";
import test from "node:test";

import {
  MAX_SCALE,
  MIN_SCALE,
  ZOOM_STEP,
  clampScale,
  computeFitScale,
  formatZoomPercent,
  scalesEqual,
  zoomBy,
  zoomIn,
  zoomOut,
} from "./imageZoom";

test("clampScale keeps values within the supported range", () => {
  assert.equal(clampScale(1), 1);
  assert.equal(clampScale(100), MAX_SCALE);
  assert.equal(clampScale(0), MIN_SCALE);
  assert.equal(clampScale(-5), MIN_SCALE);
});

test("clampScale falls back to 1 for non-finite input", () => {
  assert.equal(clampScale(Number.NaN), 1);
  assert.equal(clampScale(Number.POSITIVE_INFINITY), 1);
  assert.equal(clampScale(Number.NEGATIVE_INFINITY), 1);
});

test("computeFitScale shrinks large images to fit the container", () => {
  // 2000x1000 image inside a 1000x1000 box -> limited by width -> 0.5.
  const fit = computeFitScale(
    { width: 2000, height: 1000 },
    { width: 1000, height: 1000 },
  );
  assert.equal(fit, 0.5);
});

test("computeFitScale never upscales small images past natural size", () => {
  const fit = computeFitScale(
    { width: 100, height: 100 },
    { width: 1000, height: 1000 },
  );
  assert.equal(fit, 1);
});

test("computeFitScale is defensive about unknown dimensions", () => {
  assert.equal(
    computeFitScale({ width: 0, height: 0 }, { width: 10, height: 10 }),
    1,
  );
  assert.equal(
    computeFitScale({ width: 10, height: 10 }, { width: 0, height: 0 }),
    1,
  );
});

test("zoomIn and zoomOut step by the configured factor and round-trip", () => {
  assert.equal(zoomIn(1), ZOOM_STEP);
  assert.equal(zoomOut(1), 1 / ZOOM_STEP);
  assert.ok(scalesEqual(zoomOut(zoomIn(1)), 1));
});

test("zoom steps saturate at the range bounds", () => {
  assert.equal(zoomIn(MAX_SCALE), MAX_SCALE);
  assert.equal(zoomOut(MIN_SCALE), MIN_SCALE);
});

test("zoomBy applies an arbitrary factor with clamping", () => {
  assert.ok(scalesEqual(zoomBy(1, 2), 2));
  assert.equal(zoomBy(1, 1000), MAX_SCALE);
});

test("formatZoomPercent renders whole-number percentages", () => {
  assert.equal(formatZoomPercent(1), 100);
  assert.equal(formatZoomPercent(1.25), 125);
  assert.equal(formatZoomPercent(0.333), 33);
});
