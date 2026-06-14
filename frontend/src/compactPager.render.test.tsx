// Render / interaction tests for the compact (phone) turn/page pager.
//
// The desktop pager is a 7-control stepper; on a compact viewport it collapses
// to one always-present position button that opens the identical controls in a
// bottom sheet. The source-grep guards in mobileShell.test.ts prove the wiring
// exists; this proves it BEHAVES, in a real DOM with real clicks:
//   - clicking the position button opens the bottom sheet with the full
//     navigation (stepper + picker + stats toggle),
//   - a nav control navigates to the chosen turn/page AND dismisses the sheet
//     (the close-on-navigate the design-system requires of the drawer too),
//   - an empty session renders a disabled "No turns" button that never opens
//     (the transcript-navigation "never hidden" invariant in compact form),
//   - a desktop viewport renders the inline stepper, not the sheet button.
// See docs/features/transcript-navigation "Compact transcript pager".
import { afterEach, expect, test, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { RunTurnViewControls } from "./App.tsx";

// jsdom does not implement matchMedia; useViewport reads it to decide compact.
// matches:true => isCompact/isPhone true (the phone branch); false => desktop.
function setViewport(matches: boolean): void {
  window.matchMedia = ((query: string) => ({
    matches,
    media: query,
    onchange: null,
    addEventListener: () => {},
    removeEventListener: () => {},
    addListener: () => {},
    removeListener: () => {},
    dispatchEvent: () => false,
  })) as unknown as typeof window.matchMedia;
}

afterEach(() => {
  vi.restoreAllMocks();
  window.matchMedia = undefined as unknown as typeof window.matchMedia;
});

type Turn = Parameters<typeof RunTurnViewControls>[0]["turns"][number];

function makeTurn(turnId: string, turnNumber: number, label: string): Turn {
  return {
    turnId,
    turnNumber,
    label,
    summary: "",
    entries: [],
    active: false,
    loaded: true,
    costEstimate: null,
    contextTokens: null,
    model: null,
    effort: null,
  };
}

test("compact: the pager button opens the bottom sheet and a nav control navigates + closes it", async () => {
  setViewport(true);
  const user = userEvent.setup();
  const onNavigate = vi.fn();

  render(
    <RunTurnViewControls
      turns={[makeTurn("t1", 1, "Turn 1"), makeTurn("t2", 2, "Turn 2")]}
      selectedTurnId="t1"
      turnActivityLoadsByTurn={{}}
      statsExpanded={false}
      onStatsExpandedChange={vi.fn()}
      onNavigate={onNavigate}
    />,
  );

  // The single position button is the only control on screen; the sheet and its
  // stepper are not mounted until it is opened.
  const trigger = screen.getByRole("button", {
    name: "Turn and page navigation",
  });
  expect(trigger).toHaveTextContent("Turn 1");
  expect(trigger).toHaveTextContent("Page 1");
  expect(screen.queryByRole("dialog")).toBeNull();
  expect(
    screen.queryByRole("button", { name: "Last page of conversation" }),
  ).toBeNull();

  // Open it: the bottom sheet appears with the full stepper + stats toggle.
  await user.click(trigger);
  expect(await screen.findByRole("dialog")).toBeInTheDocument();
  expect(
    screen.getByRole("button", { name: "First page of conversation" }),
  ).toBeInTheDocument();
  expect(
    screen.getByRole("button", { name: "Last page of conversation" }),
  ).toBeInTheDocument();
  expect(
    screen.getByRole("button", { name: "Show turn stats" }),
  ).toBeInTheDocument();

  // A nav control navigates to the chosen turn/page AND dismisses the sheet.
  await user.click(
    screen.getByRole("button", { name: "Last page of conversation" }),
  );
  expect(onNavigate).toHaveBeenCalledWith("t2", 1);
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
});

test("compact: an empty session shows a disabled 'No turns' pager that never opens", async () => {
  setViewport(true);
  const user = userEvent.setup();

  render(
    <RunTurnViewControls
      turns={[]}
      selectedTurnId={null}
      turnActivityLoadsByTurn={{}}
      statsExpanded={false}
      onStatsExpandedChange={vi.fn()}
      onNavigate={vi.fn()}
    />,
  );

  const trigger = screen.getByRole("button", {
    name: "Turn and page navigation",
  });
  expect(trigger).toBeDisabled();
  expect(trigger).toHaveTextContent("No turns");

  await user.click(trigger);
  expect(screen.queryByRole("dialog")).toBeNull();
});

test("desktop: the inline stepper renders, not the compact sheet button", () => {
  setViewport(false);

  render(
    <RunTurnViewControls
      turns={[makeTurn("t1", 1, "Turn 1")]}
      selectedTurnId="t1"
      turnActivityLoadsByTurn={{}}
      statsExpanded={false}
      onStatsExpandedChange={vi.fn()}
      onNavigate={vi.fn()}
    />,
  );

  // Desktop shows the always-visible stepper inline; there is no compact
  // position-button / sheet on a wide viewport.
  expect(
    screen.queryByRole("button", { name: "Turn and page navigation" }),
  ).toBeNull();
  expect(
    screen.getByRole("button", { name: "Last page of conversation" }),
  ).toBeInTheDocument();
});
