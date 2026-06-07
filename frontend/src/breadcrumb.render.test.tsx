// Render / interaction tests for the breadcrumb title chrome (jsdom — .test.tsx).
//
// The pure trail derivation — which crumbs exist, their hrefs, and which one is
// current — is unit-tested in breadcrumb.test.ts. This layer proves the React
// chrome turns that trail into the right DOM: climbable <a> links for the
// navigable ancestors, a plain structural label for "pages", and a
// non-interactive current leaf. It also proves a plain click navigates in-app
// (pushState, no full page load) while a modified click is left to the browser
// so cmd/ctrl-click still opens a new tab. See docs/testing.md → "Frontend test
// layers".
import { afterEach, expect, test, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { BreadcrumbLink, WorkspaceBreadcrumbTrail } from "./App.tsx";
import type { BreadcrumbLocation } from "./breadcrumb";

afterEach(() => vi.restoreAllMocks());

const location = (over: Partial<BreadcrumbLocation>): BreadcrumbLocation => ({
  tab: "turns",
  turnNumber: null,
  pageNumber: null,
  staticPath: null,
  turnUnavailable: false,
  ...over,
});

test("a turn+page trail renders climbable links, a structural label, and a non-interactive leaf", () => {
  render(
    <WorkspaceBreadcrumbTrail
      sessionId="s1"
      location={location({ tab: "turns", turnNumber: 6, pageNumber: 2 })}
    />,
  );

  // The section and the turn number are climbable ancestor links. The bare
  // "turns" crumb targets the canonical turns root (/sessions/{id}); the turn
  // number targets that specific turn.
  expect(
    screen.getByRole("link", { name: "turns" }).getAttribute("href"),
  ).toMatch(/\/sessions\/s1$/);
  expect(
    screen.getByRole("link", { name: "6" }).getAttribute("href"),
  ).toMatch(/\/sessions\/s1\/turns\/6$/);

  // "pages" is a structural label, never a link.
  expect(screen.queryByRole("link", { name: "pages" })).toBeNull();
  expect(screen.getByText("pages")).toBeInTheDocument();

  // The current page is the non-interactive leaf: no link, marked aria-current,
  // so a crumb never links to where you already are.
  expect(screen.queryByRole("link", { name: "2" })).toBeNull();
  expect(screen.getByText("2")).toHaveAttribute("aria-current", "page");
});

test("a plain click on a crumb navigates in-app via pushState (no full page load)", async () => {
  const user = userEvent.setup();
  const pushState = vi
    .spyOn(window.history, "pushState")
    .mockImplementation(() => {});
  const href = `${window.location.origin}/sessions/s1/turns/6`;

  render(<BreadcrumbLink href={href} label="6" />);
  await user.click(screen.getByRole("link", { name: "6" }));

  // The crumb pushed the target route in-app rather than triggering a navigation.
  expect(pushState).toHaveBeenCalledWith({}, "", href);
});

test("a modified click is left to the browser so cmd/ctrl-click opens a new tab", async () => {
  const user = userEvent.setup();
  const pushState = vi
    .spyOn(window.history, "pushState")
    .mockImplementation(() => {});
  const href = `${window.location.origin}/sessions/s1/turns/6`;

  render(<BreadcrumbLink href={href} label="6" />);
  // Hold Meta so the onClick guard bails and the anchor's default behavior runs.
  await user.keyboard("{Meta>}");
  await user.click(screen.getByRole("link", { name: "6" }));
  await user.keyboard("{/Meta}");

  expect(pushState).not.toHaveBeenCalled();
});
