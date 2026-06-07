import { test, expect } from "vitest";

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
  expect(s.page).toBe(2);
  expect(s.pageCount).toBe(3);
  s = turnActivityPagerState(info(9, 3)); // clamps above the top
  expect(s.page).toBe(3);
  expect(s.pageCount).toBe(3);
  s = turnActivityPagerState(info(0, 3)); // clamps below the first
  expect(s.page).toBe(1);
  expect(s.pageCount).toBe(3);
  s = turnActivityPagerState(info(1, 1));
  expect(s.page).toBe(1);
  expect(s.pageCount).toBe(1);
  s = turnActivityPagerState(undefined); // default 1/1 so the dropdown can render disabled pre-load
  expect(s.page).toBe(1);
  expect(s.pageCount).toBe(1);
});

test("no page directory yet → pager hidden (nothing to show during load)", () => {
  const state = turnActivityPagerState(undefined);
  expect(state.visible).toBe(false);
  expect(state.label).toBe("");
});

test("an empty turn (zero pages) → pager hidden, since there is no body to navigate", () => {
  const state = turnActivityPagerState(info(0, 0));
  expect(state.visible).toBe(false);
});

test("a single-page turn still shows the pager, disabled, reading 'page 1 of 1'", () => {
  // The load-bearing case: the affordance must be present and visibly
  // limited, not hidden — a hidden control reads as "this feature is absent".
  const state = turnActivityPagerState(info(1, 1));
  expect(state.visible).toBe(true);
  expect(state.label).toBe("page 1 of 1");
  expect(state.canPageOlder).toBe(false);
  expect(state.canPageNewer).toBe(false);
});

test("first page of many → only › (newer) is actionable", () => {
  const state = turnActivityPagerState(info(1, 3));
  expect(state.visible).toBe(true);
  expect(state.label).toBe("page 1 of 3");
  expect(state.canPageOlder).toBe(false);
  expect(state.canPageNewer).toBe(true);
  expect(state.newerPage).toBe(2);
});

test("a middle page → both directions actionable and target the neighbours", () => {
  const state = turnActivityPagerState(info(2, 3));
  expect(state.canPageOlder).toBe(true);
  expect(state.canPageNewer).toBe(true);
  expect(state.olderPage).toBe(1);
  expect(state.newerPage).toBe(3);
});

test("the last page (the default landing page) → only ‹ (older) is actionable", () => {
  const state = turnActivityPagerState(info(3, 3));
  expect(state.label).toBe("page 3 of 3");
  expect(state.canPageOlder).toBe(true);
  expect(state.canPageNewer).toBe(false);
  expect(state.olderPage).toBe(2);
});

test("a current page past the end clamps to the last page", () => {
  const state = turnActivityPagerState(info(9, 3));
  expect(state.label).toBe("page 3 of 3");
  expect(state.canPageNewer).toBe(false);
  expect(state.canPageOlder).toBe(true);
});

test("a current page below 1 clamps to the first page", () => {
  const state = turnActivityPagerState(info(0, 3));
  expect(state.label).toBe("page 1 of 3");
  expect(state.canPageOlder).toBe(false);
  expect(state.canPageNewer).toBe(true);
});

test("page stepper exposes first/last jump targets with boundary-aware enablement", () => {
  // Middle page: every direction is reachable; ends target 1 and pageCount.
  let s = turnActivityPagerState(info(2, 3));
  expect(s.firstPage).toBe(1);
  expect(s.lastPage).toBe(3);
  expect(s.canPageFirst).toBe(true);
  expect(s.canPageLast).toBe(true);

  // On the first page: first is inert, last is reachable.
  s = turnActivityPagerState(info(1, 3));
  expect(s.canPageFirst).toBe(false);
  expect(s.canPageLast).toBe(true);
  expect(s.firstPage).toBe(1);
  expect(s.lastPage).toBe(3);

  // On the last (default) page: last is inert, first is reachable.
  s = turnActivityPagerState(info(3, 3));
  expect(s.canPageFirst).toBe(true);
  expect(s.canPageLast).toBe(false);

  // Single page: both jumps disabled but the targets are still well-defined.
  s = turnActivityPagerState(info(1, 1));
  expect(s.canPageFirst).toBe(false);
  expect(s.canPageLast).toBe(false);
  expect(s.firstPage).toBe(1);
  expect(s.lastPage).toBe(1);
});

test("non-finite inputs are treated as no directory (hidden), never NaN labels", () => {
  expect(turnActivityPagerState(info(Number.NaN, 3)).visible).toBe(false);
  expect(turnActivityPagerState(info(1, Number.NaN)).visible).toBe(false);
  expect(turnActivityPagerState(info(1, Number.POSITIVE_INFINITY)).visible).toBe(false);
});

test("event progress defaults to an explicit zero of the 1000-event page limit", () => {
  const progress = turnActivityEventProgress(undefined, 1);
  expect(progress.eventCount).toBe(0);
  expect(progress.limit).toBe(TURN_ACTIVITY_PAGE_EVENT_LIMIT);
  expect(progress.label).toBe("0/1000 events");
  expect(progress.totalLabel).toBe(null);
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
  expect(progress.eventCount).toBe(1000);
  expect(progress.label).toBe("1000/1000 events");
  expect(progress.totalLabel).toBe("2375 total");
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
  expect(progress.label).toBe("42/1000 events");
  expect(progress.totalLabel).toBe(null);
});
