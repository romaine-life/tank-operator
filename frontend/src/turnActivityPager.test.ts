import assert from "node:assert/strict";
import { test } from "node:test";

import {
  TURN_ACTIVITY_PAGE_EVENT_LIMIT,
  turnActivityEventProgress,
  turnActivityPagerState,
  type TurnActivityPageInfo,
} from "./turnActivityPager.ts";

function info(page: number, pageCount: number): TurnActivityPageInfo {
  return { page, pageCount };
}

test("exposes clamped current page + total pageCount for the Page dropdown", () => {
  let s = turnActivityPagerState(info(2, 3));
  assert.equal(s.page, 2);
  assert.equal(s.pageCount, 3);
  s = turnActivityPagerState(info(9, 3)); // clamps above the top
  assert.equal(s.page, 3);
  assert.equal(s.pageCount, 3);
  s = turnActivityPagerState(info(0, 3)); // clamps below the first
  assert.equal(s.page, 1);
  assert.equal(s.pageCount, 3);
  s = turnActivityPagerState(info(1, 1));
  assert.equal(s.page, 1);
  assert.equal(s.pageCount, 1);
  s = turnActivityPagerState(undefined); // default 1/1 so the dropdown can render disabled pre-load
  assert.equal(s.page, 1);
  assert.equal(s.pageCount, 1);
});

test("no page directory yet → pager hidden (nothing to show during load)", () => {
  const state = turnActivityPagerState(undefined);
  assert.equal(state.visible, false);
  assert.equal(state.label, "");
});

test("an empty turn (zero pages) → pager hidden, since there is no body to navigate", () => {
  const state = turnActivityPagerState(info(0, 0));
  assert.equal(state.visible, false);
});

test("a single-page turn still shows the pager, disabled, reading 'page 1 of 1'", () => {
  // The load-bearing case: the affordance must be present and visibly
  // limited, not hidden — a hidden control reads as "this feature is absent".
  const state = turnActivityPagerState(info(1, 1));
  assert.equal(state.visible, true);
  assert.equal(state.label, "page 1 of 1");
  assert.equal(state.canPageOlder, false);
  assert.equal(state.canPageNewer, false);
});

test("first page of many → only › (newer) is actionable", () => {
  const state = turnActivityPagerState(info(1, 3));
  assert.equal(state.visible, true);
  assert.equal(state.label, "page 1 of 3");
  assert.equal(state.canPageOlder, false);
  assert.equal(state.canPageNewer, true);
  assert.equal(state.newerPage, 2);
});

test("a middle page → both directions actionable and target the neighbours", () => {
  const state = turnActivityPagerState(info(2, 3));
  assert.equal(state.canPageOlder, true);
  assert.equal(state.canPageNewer, true);
  assert.equal(state.olderPage, 1);
  assert.equal(state.newerPage, 3);
});

test("the last page (the default landing page) → only ‹ (older) is actionable", () => {
  const state = turnActivityPagerState(info(3, 3));
  assert.equal(state.label, "page 3 of 3");
  assert.equal(state.canPageOlder, true);
  assert.equal(state.canPageNewer, false);
  assert.equal(state.olderPage, 2);
});

test("a current page past the end clamps to the last page", () => {
  const state = turnActivityPagerState(info(9, 3));
  assert.equal(state.label, "page 3 of 3");
  assert.equal(state.canPageNewer, false);
  assert.equal(state.canPageOlder, true);
});

test("a current page below 1 clamps to the first page", () => {
  const state = turnActivityPagerState(info(0, 3));
  assert.equal(state.label, "page 1 of 3");
  assert.equal(state.canPageOlder, false);
  assert.equal(state.canPageNewer, true);
});

test("page stepper exposes first/last jump targets with boundary-aware enablement", () => {
  // Middle page: every direction is reachable; ends target 1 and pageCount.
  let s = turnActivityPagerState(info(2, 3));
  assert.equal(s.firstPage, 1);
  assert.equal(s.lastPage, 3);
  assert.equal(s.canPageFirst, true);
  assert.equal(s.canPageLast, true);

  // On the first page: first is inert, last is reachable.
  s = turnActivityPagerState(info(1, 3));
  assert.equal(s.canPageFirst, false);
  assert.equal(s.canPageLast, true);
  assert.equal(s.firstPage, 1);
  assert.equal(s.lastPage, 3);

  // On the last (default) page: last is inert, first is reachable.
  s = turnActivityPagerState(info(3, 3));
  assert.equal(s.canPageFirst, true);
  assert.equal(s.canPageLast, false);

  // Single page: both jumps disabled but the targets are still well-defined.
  s = turnActivityPagerState(info(1, 1));
  assert.equal(s.canPageFirst, false);
  assert.equal(s.canPageLast, false);
  assert.equal(s.firstPage, 1);
  assert.equal(s.lastPage, 1);
});

test("non-finite inputs are treated as no directory (hidden), never NaN labels", () => {
  assert.equal(turnActivityPagerState(info(Number.NaN, 3)).visible, false);
  assert.equal(turnActivityPagerState(info(1, Number.NaN)).visible, false);
  assert.equal(turnActivityPagerState(info(1, Number.POSITIVE_INFINITY)).visible, false);
});

test("event progress defaults to an explicit zero of the 1000-event page limit", () => {
  const progress = turnActivityEventProgress(undefined, 1);
  assert.equal(progress.eventCount, 0);
  assert.equal(progress.limit, TURN_ACTIVITY_PAGE_EVENT_LIMIT);
  assert.equal(progress.label, "0/1000 events");
  assert.equal(progress.totalLabel, null);
});

test("event progress reports the selected page count against the page limit", () => {
  const progress = turnActivityEventProgress(
    {
      page: 2,
      pageCount: 3,
      totalEventCount: 2375,
      pages: [
        { number: 1, eventCount: 1000, sealed: true },
        { number: 2, eventCount: 1000, sealed: true },
        { number: 3, eventCount: 375, sealed: false },
      ],
    },
    2,
  );
  assert.equal(progress.eventCount, 1000);
  assert.equal(progress.label, "1000/1000 events");
  assert.equal(progress.totalLabel, "2375 total");
});

test("event progress omits the total badge when a turn fits within one page", () => {
  const progress = turnActivityEventProgress(
    {
      page: 1,
      pageCount: 1,
      totalEventCount: 42,
      pages: [{ number: 1, eventCount: 42, sealed: true }],
    },
    1,
  );
  assert.equal(progress.label, "42/1000 events");
  assert.equal(progress.totalLabel, null);
});
