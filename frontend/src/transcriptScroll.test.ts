import assert from "node:assert/strict";
import { test } from "node:test";

import {
  TRANSCRIPT_VISUAL_BOTTOM_THRESHOLD_PX,
  transcriptBottomDistance,
  transcriptVisuallyAtBottom,
} from "./transcriptScroll.ts";

test("transcript visual bottom follows the actual scroll container distance", () => {
  assert.equal(
    transcriptVisuallyAtBottom(
      { scrollHeight: 5561, clientHeight: 605, scrollTop: 4681 },
      true,
    ),
    false,
  );
  assert.equal(
    transcriptBottomDistance({ scrollHeight: 5561, clientHeight: 605, scrollTop: 4681 }),
    275,
  );
  assert.equal(
    transcriptVisuallyAtBottom(
      {
        scrollHeight: 5561,
        clientHeight: 605,
        scrollTop: 5561 - 605 - TRANSCRIPT_VISUAL_BOTTOM_THRESHOLD_PX,
      },
      false,
    ),
    true,
  );
});

test("transcript visual bottom falls back when no scroll container is mounted", () => {
  assert.equal(transcriptVisuallyAtBottom(null, true), true);
  assert.equal(transcriptVisuallyAtBottom(undefined, false), false);
});
