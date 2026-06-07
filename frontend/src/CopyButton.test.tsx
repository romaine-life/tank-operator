// Exemplar component/interaction test (jsdom project — `.test.tsx`).
//
// Pattern shown here: a stateful button with an async side effect (clipboard)
// and a time-based reset. It demonstrates the house conventions:
//   - render the real component and query by ACCESSIBLE role/name, never a
//     classname or test id;
//   - drive it with `userEvent` (not `fireEvent`). `userEvent.setup()` installs
//     a working clipboard stub, so we assert the copy by reading it back rather
//     than by spying on a private call;
//   - assert on user-visible output (the aria-label assistive tech announces);
//   - reach for fake timers ONLY when the test is about elapsed time (the 1.5s
//     reset), and give user-event the `advanceTimers` bridge so its async event
//     sequence still progresses while timers are faked.
//
// See docs/testing.md → "Frontend test layers" for the full conventions.
import { afterEach, expect, test, vi } from "vitest";
import { act, fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { CopyButton } from "./App.tsx";

afterEach(() => {
  vi.useRealTimers();
});

test("renders an accessible copy control", () => {
  render(<CopyButton text="hello world" />);
  expect(
    screen.getByRole("button", { name: "Copy message" }),
  ).toBeInTheDocument();
});

test("clicking copies the text and announces the copied state", async () => {
  const user = userEvent.setup();
  render(<CopyButton text="hello world" />);

  await user.click(screen.getByRole("button", { name: "Copy message" }));

  // The text really landed on the clipboard (read back from user-event's stub).
  expect(await navigator.clipboard.readText()).toBe("hello world");
  // ...and the accessible name flips so assistive tech announces success.
  expect(
    await screen.findByRole("button", { name: "Copied" }),
  ).toBeInTheDocument();
});

test("the copied state reverts after the 1.5s window", async () => {
  // Deliberate exception to "userEvent over fireEvent": this test isolates the
  // component's `setTimeout(…, 1500)` reset, and user-event's async event
  // pipeline does not compose cleanly with faked timers. `fireEvent` is a
  // synchronous, scheduler-safe gesture, so under fake timers it is the right
  // tool here. Use it ONLY for this kind of timer-isolation test.
  vi.useFakeTimers();
  // user-event isn't driving the click, so it won't install its clipboard stub;
  // give the component a resolved clipboard so the success path runs.
  const clipboard = Object.getOwnPropertyDescriptor(navigator, "clipboard");
  Object.defineProperty(navigator, "clipboard", {
    configurable: true,
    value: { writeText: vi.fn().mockResolvedValue(undefined) },
  });

  try {
    render(<CopyButton text="hello world" />);
    // act() flushes the async onClick (clipboard await + setCopied) so the
    // "Copied" state has committed before we assert.
    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "Copy message" }));
    });
    expect(screen.getByRole("button", { name: "Copied" })).toBeInTheDocument();

    // Drive the 1.5s reset timer to completion deterministically.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1500);
    });
    expect(
      screen.getByRole("button", { name: "Copy message" }),
    ).toBeInTheDocument();
  } finally {
    if (clipboard) {
      Object.defineProperty(navigator, "clipboard", clipboard);
    } else {
      delete (navigator as { clipboard?: unknown }).clipboard;
    }
  }
});
