import assert from "node:assert/strict";
import { test } from "node:test";

import {
  BP_COMPACT,
  BP_PHONE,
  MQ_COMPACT,
  MQ_PHONE,
  isCompactWidth,
  isPhoneWidth,
} from "./breakpoints.ts";

test("canonical compact/phone breakpoints are the documented widths", () => {
  assert.equal(BP_COMPACT, 768);
  assert.equal(BP_PHONE, 640);
  // The phone tier must sit strictly inside the compact tier.
  assert.ok(BP_PHONE < BP_COMPACT);
});

test("media queries derive from the constants so JS and CSS cannot drift", () => {
  assert.equal(MQ_COMPACT, "(max-width: 768px)");
  assert.equal(MQ_PHONE, "(max-width: 640px)");
});

test("width predicates treat the breakpoint as inclusive-compact", () => {
  assert.equal(isCompactWidth(390), true); // common phone
  assert.equal(isCompactWidth(768), true); // boundary is compact
  assert.equal(isCompactWidth(769), false); // desktop
  assert.equal(isPhoneWidth(640), true);
  assert.equal(isPhoneWidth(641), false);
});
