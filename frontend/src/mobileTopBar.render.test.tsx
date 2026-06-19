// Render / interaction tests for the compact-shell top bar (the off-canvas
// session drawer's trigger + the compact back/location affordances).
//
// The drawer PANEL is a vetted Radix Sheet wired into AuthenticatedApp's shell;
// what's ours to prove here is the bar's behavior: the hamburger and the context
// chip both open the drawer (onOpenNav), a sub-location renders a back affordance
// that climbs (onBack), the compact current-location label replaces the full
// breadcrumb trail, and home shows the brand with no back. These run in jsdom
// with real clicks. See docs/features/app-chrome "Mobile Session Triage" and
// "Session Breadcrumb Title Bar".
import { afterEach, expect, test, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { MobileTopBar } from "./MobileTopBar.tsx";

afterEach(() => vi.restoreAllMocks());

test("the hamburger opens the session drawer (home: brand, no back affordance)", async () => {
  const user = userEvent.setup();
  const onOpenNav = vi.fn();

  render(<MobileTopBar isHome onOpenNav={onOpenNav} />);

  // Home shows the brand and never a back affordance (nowhere to climb to).
  expect(screen.getByText("tank-operator")).toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "Back" })).toBeNull();

  // The hamburger is the drawer trigger. (The context chip carries the same
  // label/handler; both open the drawer.)
  const openControls = screen.getAllByRole("button", { name: "Open sessions" });
  await user.click(openControls[0]);
  expect(onOpenNav).toHaveBeenCalledTimes(1);
});

test("a sub-location shows the compact location label and a back affordance that climbs", async () => {
  const user = userEvent.setup();
  const onOpenNav = vi.fn();
  const onBack = vi.fn();

  render(
    <MobileTopBar
      isHome={false}
      sessionName="annoying jolt"
      locationLabel="turns / 1 / pages / 1"
      statusDotClass="status-dot status-agent-working"
      statusLabel="Agent working"
      onOpenNav={onOpenNav}
      onBack={onBack}
    />,
  );

  // The compact shell shows the current-location label in place of the full
  // breadcrumb trail; the bare session name is not shown when a location is set.
  expect(screen.getByText("turns / 1 / pages / 1")).toBeInTheDocument();
  expect(screen.queryByText("annoying jolt")).toBeNull();

  // Back climbs to the parent location without opening the drawer.
  await user.click(screen.getByRole("button", { name: "Back" }));
  expect(onBack).toHaveBeenCalledTimes(1);
  expect(onOpenNav).not.toHaveBeenCalled();
});

test("with no sub-location the bar falls back to the session name", () => {
  render(
    <MobileTopBar
      isHome={false}
      sessionName="annoying jolt"
      onOpenNav={vi.fn()}
    />,
  );

  expect(screen.getByText("annoying jolt")).toBeInTheDocument();
  // No onBack provided -> no back affordance.
  expect(screen.queryByRole("button", { name: "Back" })).toBeNull();
});
