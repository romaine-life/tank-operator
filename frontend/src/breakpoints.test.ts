import { test, expect } from "vitest";

import {
  BP_COMPACT,
  BP_PHONE,
  MQ_COMPACT,
  MQ_PHONE,
  isCompactWidth,
  isPhoneWidth,
} from "./breakpoints.ts";

test("canonical compact/phone breakpoints are the documented widths", () => {
  expect(BP_COMPACT).toBe(768);
  expect(BP_PHONE).toBe(640);
  // The phone tier must sit strictly inside the compact tier.
  expect(BP_PHONE < BP_COMPACT).toBeTruthy();
});

test("media queries derive from the constants so JS and CSS cannot drift", () => {
  expect(MQ_COMPACT).toBe("(max-width: 768px)");
  expect(MQ_PHONE).toBe("(max-width: 640px)");
});

test("width predicates treat the breakpoint as inclusive-compact", () => {
  expect(isCompactWidth(390)).toBe(true); // common phone
  expect(isCompactWidth(768)).toBe(true); // boundary is compact
  expect(isCompactWidth(769)).toBe(false); // desktop
  expect(isPhoneWidth(640)).toBe(true);
  expect(isPhoneWidth(641)).toBe(false);
});
