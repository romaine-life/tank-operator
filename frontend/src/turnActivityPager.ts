// Turn activity pagination affordance.
//
// A long agent turn splits its activity body into pages sealed at the
// per-turn event threshold (see backend `turnPageEventLimit`). The per-turn
// /activity endpoint (`server_turn_activity_v2`) returns the page directory
// (`page`, `page_count`, `pages[]`) and defaults to the last page.
//
// This module owns the single rule for how that directory becomes a rendered
// pager. The rule is deliberately *not* "show the pager once there is more
// than one page": a control that only materialises past the (invisible)
// 1000-event seal is indistinguishable from the feature being absent — which
// is exactly how a reader experiences it on a normal-length turn. The honest,
// discoverable affordance is an always-present selector that *disables* its
// navigation when there is no other page to reach ("page 1 of 1", greyed
// ‹ ›), and enables ‹ / › only when a sealed older / newer page exists.
//
// Keeping the decision here (pure + unit-tested) is also the regression guard:
// the test pins `visible: true` at a single page, so a future change cannot
// quietly reintroduce the threshold-gated appear/disappear behaviour without
// failing a test.

export type TurnActivityPageInfo = {
  page: number;
  pageCount: number;
};

export type TurnActivityPagerState = {
  // Whether to render the pager at all. False only when there is no page
  // directory yet (info absent / not-yet-loaded) or it reports zero pages (an
  // empty turn with no activity body to navigate) — never merely because the
  // turn happens to fit on a single page.
  visible: boolean;
  // Human label, e.g. "page 2 of 5". Empty when not visible.
  label: string;
  // Whether the ‹ (older) / › (newer) controls are actionable.
  canPageOlder: boolean;
  canPageNewer: boolean;
  // The page each control navigates to (clamped in-range). When the matching
  // can* flag is false these equal the current page, so a stray click is inert.
  olderPage: number;
  newerPage: number;
};

const HIDDEN: TurnActivityPagerState = {
  visible: false,
  label: "",
  canPageOlder: false,
  canPageNewer: false,
  olderPage: 1,
  newerPage: 1,
};

export function turnActivityPagerState(
  pageInfo: TurnActivityPageInfo | undefined,
): TurnActivityPagerState {
  if (!pageInfo) return HIDDEN;
  const { page, pageCount } = pageInfo;
  if (!Number.isFinite(page) || !Number.isFinite(pageCount)) return HIDDEN;

  const totalPages = Math.floor(pageCount);
  // Zero pages means there is genuinely no body to navigate (empty turn); that
  // is a real absence, not the threshold-hidden illusion we are avoiding.
  if (totalPages < 1) return HIDDEN;

  const current = Math.min(Math.max(1, Math.floor(page)), totalPages);
  return {
    visible: true,
    label: `page ${current} of ${totalPages}`,
    canPageOlder: current > 1,
    canPageNewer: current < totalPages,
    olderPage: Math.max(1, current - 1),
    newerPage: Math.min(totalPages, current + 1),
  };
}
