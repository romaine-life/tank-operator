import { test, expect } from "vitest";

import { createStreamBackoff } from "./streamBackoff";

// random() === 0.5 makes the jitter multiplier exactly 1, so delays
// equal the raw doubled/capped values.
const midpointRandom = () => 0.5;

test("nextDelay doubles from baseMs on consecutive failures", () => {
  const backoff = createStreamBackoff({
    baseMs: 1000,
    maxMs: 30000,
    random: midpointRandom,
  });
  expect(backoff.nextDelay()).toBe(1000);
  expect(backoff.nextDelay()).toBe(2000);
  expect(backoff.nextDelay()).toBe(4000);
  expect(backoff.nextDelay()).toBe(8000);
  expect(backoff.nextDelay()).toBe(16000);
});

test("nextDelay caps at maxMs and stays capped", () => {
  const backoff = createStreamBackoff({
    baseMs: 1000,
    maxMs: 30000,
    random: midpointRandom,
  });
  for (let i = 0; i < 5; i += 1) backoff.nextDelay(); // 1s..16s
  expect(backoff.nextDelay()).toBe(30000);
  // A long outage must not grow past the cap (or overflow the exponent).
  for (let i = 0; i < 100; i += 1) {
    expect(backoff.nextDelay()).toBe(30000);
  }
});

test("cap respects a maxMs that is not a power-of-two multiple of baseMs", () => {
  const backoff = createStreamBackoff({
    baseMs: 1000,
    maxMs: 5000,
    random: midpointRandom,
  });
  expect(backoff.nextDelay()).toBe(1000);
  expect(backoff.nextDelay()).toBe(2000);
  expect(backoff.nextDelay()).toBe(4000);
  expect(backoff.nextDelay()).toBe(5000);
  expect(backoff.nextDelay()).toBe(5000);
});

test("jitter bounds: random()=0 gives -jitterRatio, random()=1 gives +jitterRatio", () => {
  const low = createStreamBackoff({
    baseMs: 1000,
    jitterRatio: 0.2,
    random: () => 0,
  });
  expect(low.nextDelay()).toBe(800); // 1000 * (1 - 0.2)
  expect(low.nextDelay()).toBe(1600); // 2000 * (1 - 0.2)

  const high = createStreamBackoff({
    baseMs: 1000,
    jitterRatio: 0.2,
    random: () => 1,
  });
  expect(high.nextDelay()).toBe(1200); // 1000 * (1 + 0.2)
  expect(high.nextDelay()).toBe(2400); // 2000 * (1 + 0.2)
});

test("jitter applies after the cap", () => {
  const low = createStreamBackoff({
    baseMs: 1000,
    maxMs: 30000,
    jitterRatio: 0.2,
    random: () => 0,
  });
  const high = createStreamBackoff({
    baseMs: 1000,
    maxMs: 30000,
    jitterRatio: 0.2,
    random: () => 1,
  });
  for (let i = 0; i < 10; i += 1) {
    low.nextDelay();
    high.nextDelay();
  }
  expect(low.nextDelay()).toBe(24000); // 30000 * (1 - 0.2)
  expect(high.nextDelay()).toBe(36000); // 30000 * (1 + 0.2)
});

test("every delay stays within the ±jitterRatio envelope", () => {
  // Deterministic-but-varied random source.
  let seed = 0;
  const cyclingRandom = () => {
    seed = (seed * 31 + 17) % 97;
    return seed / 97;
  };
  const backoff = createStreamBackoff({
    baseMs: 250,
    maxMs: 8000,
    jitterRatio: 0.2,
    random: cyclingRandom,
  });
  let expectedRaw = 250;
  for (let i = 0; i < 50; i += 1) {
    const delay = backoff.nextDelay();
    expect(delay).toBeGreaterThanOrEqual(Math.floor(expectedRaw * 0.8));
    expect(delay).toBeLessThanOrEqual(Math.ceil(expectedRaw * 1.2));
    expectedRaw = Math.min(expectedRaw * 2, 8000);
  }
});

test("zero jitterRatio yields exact doubling", () => {
  const backoff = createStreamBackoff({
    baseMs: 500,
    maxMs: 4000,
    jitterRatio: 0,
    random: () => 0.99,
  });
  expect(backoff.nextDelay()).toBe(500);
  expect(backoff.nextDelay()).toBe(1000);
  expect(backoff.nextDelay()).toBe(2000);
  expect(backoff.nextDelay()).toBe(4000);
  expect(backoff.nextDelay()).toBe(4000);
});

test("reset returns the schedule to baseMs", () => {
  const backoff = createStreamBackoff({
    baseMs: 1000,
    maxMs: 30000,
    random: midpointRandom,
  });
  backoff.nextDelay();
  backoff.nextDelay();
  backoff.nextDelay();
  backoff.reset();
  expect(backoff.nextDelay()).toBe(1000);
  expect(backoff.nextDelay()).toBe(2000);
});

test("reset after hitting the cap returns to baseMs", () => {
  const backoff = createStreamBackoff({
    baseMs: 1000,
    maxMs: 30000,
    random: midpointRandom,
  });
  for (let i = 0; i < 20; i += 1) backoff.nextDelay();
  backoff.reset();
  expect(backoff.nextDelay()).toBe(1000);
});

test("defaults: 1s base, 30s cap, ±20% jitter", () => {
  const backoff = createStreamBackoff({ random: midpointRandom });
  expect(backoff.nextDelay()).toBe(1000);
  expect(backoff.nextDelay()).toBe(2000);
  for (let i = 0; i < 20; i += 1) backoff.nextDelay();
  expect(backoff.nextDelay()).toBe(30000);

  const low = createStreamBackoff({ random: () => 0 });
  expect(low.nextDelay()).toBe(800);
});

test("instances are independent", () => {
  const a = createStreamBackoff({ random: midpointRandom });
  const b = createStreamBackoff({ random: midpointRandom });
  a.nextDelay();
  a.nextDelay();
  expect(b.nextDelay()).toBe(1000);
  a.reset();
  expect(b.nextDelay()).toBe(2000);
});
