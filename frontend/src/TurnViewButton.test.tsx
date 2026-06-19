// Exemplar component/interaction test (jsdom project — `.test.tsx`).
//
// This is the NAVIGATION pattern the breadcrumb effort (#953) will reuse: a
// deep-linkable control that navigates in-app on a plain left click but stays
// out of the way of the browser's native open-in-new-tab on a modifier or
// middle click. The same shape applies to a breadcrumb crumb: clicking a crumb
// should pushState + navigate, while ⌘/Ctrl-click should open the crumb's URL
// in a new tab.
//
// Conventions shown:
//   - query the rendered anchor by its accessible role/name (`link`), and assert
//     the real `href` exists so the control is deep-linkable and middle-click
//     works;
//   - use `userEvent` for the ordinary left-click;
//   - use `fireEvent` for the modifier/middle-click MATRIX, where the test needs
//     precise control of `button` / `metaKey` / `ctrlKey` and to inspect
//     `defaultPrevented` — that low-level event-init control is exactly what
//     fireEvent is for;
//   - assert the contract by observable effect: the in-app navigate callback
//     fires (or doesn't) and the default is prevented (or isn't).
//
// See docs/testing.md → "Frontend test layers" → "Testing navigation".
import { expect, test, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { TurnViewButton } from "./App.tsx";

const HREF = "https://tank.example/sessions/42/turns/7";

test("renders a real, deep-linkable anchor when given an href", () => {
  render(<TurnViewButton turnId="turn_abc" href={HREF} onOpenTurn={vi.fn()} />);
  const link = screen.getByRole("link", { name: "Open turn in Turns" });
  // A real href means middle-click / "open in new tab" / copy-link all work and
  // the route is shareable — the browser owns the URL, the SPA only intercepts.
  expect(link).toHaveAttribute("href", HREF);
});

test("a plain left click navigates in-app", async () => {
  const onOpenTurn = vi.fn();
  const user = userEvent.setup();
  render(<TurnViewButton turnId="turn_abc" href={HREF} onOpenTurn={onOpenTurn} />);

  await user.click(screen.getByRole("link", { name: "Open turn in Turns" }));

  expect(onOpenTurn).toHaveBeenCalledTimes(1);
  expect(onOpenTurn).toHaveBeenCalledWith("turn_abc", { anchor: "bottom" });
});

test("a plain left click suppresses the browser's full-page navigation", () => {
  render(<TurnViewButton turnId="turn_abc" href={HREF} onOpenTurn={vi.fn()} />);

  // The handler calls stopPropagation(), so a bubble-phase listener can't see
  // defaultPrevented. fireEvent.click returns the dispatchEvent result, which
  // is `false` precisely when the handler called preventDefault — i.e. the SPA
  // intercepted the click instead of letting the anchor full-page navigate.
  const notCancelled = fireEvent.click(
    screen.getByRole("link", { name: "Open turn in Turns" }),
  );
  expect(notCancelled).toBe(false);
});

// Each of these should be left entirely to the browser: the SPA must NOT call
// its in-app navigate, and must NOT preventDefault, or it would break
// open-in-new-tab / open-in-new-window.
const browserOwnedClicks: Array<[string, MouseEventInit]> = [
  ["⌘ / Meta-click", { button: 0, metaKey: true }],
  ["Ctrl-click", { button: 0, ctrlKey: true }],
  ["Shift-click", { button: 0, shiftKey: true }],
  ["Alt-click", { button: 0, altKey: true }],
  ["middle click", { button: 1 }],
];

test.each(browserOwnedClicks)(
  "%s is left to the browser (no in-app navigate, default not prevented)",
  (_label, init) => {
    const onOpenTurn = vi.fn();
    let defaultPrevented = false;
    render(
      <div onClick={(e) => (defaultPrevented = e.defaultPrevented)}>
        <TurnViewButton turnId="turn_abc" href={HREF} onOpenTurn={onOpenTurn} />
      </div>,
    );

    fireEvent.click(
      screen.getByRole("link", { name: "Open turn in Turns" }),
      init,
    );

    expect(onOpenTurn).not.toHaveBeenCalled();
    expect(defaultPrevented).toBe(false);
  },
);

test("without an href it renders a button that navigates in-app on click", async () => {
  const onOpenTurn = vi.fn();
  const user = userEvent.setup();
  render(<TurnViewButton turnId="turn_xyz" onOpenTurn={onOpenTurn} />);

  // No href → a <button>, not a link (nothing to deep-link to here).
  expect(
    screen.queryByRole("link", { name: "Open turn in Turns" }),
  ).not.toBeInTheDocument();
  await user.click(screen.getByRole("button", { name: "Open turn in Turns" }));

  expect(onOpenTurn).toHaveBeenCalledWith("turn_xyz", { anchor: "bottom" });
});
