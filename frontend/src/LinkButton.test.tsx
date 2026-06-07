// Exemplar component/interaction test (jsdom project — `.test.tsx`).
//
// Pattern shown here: a component that depends on app context (`RunContext`)
// and an async backend call, with a success AND a failure path. It demonstrates:
//   - how to render a context-consuming component by wrapping it in the real
//     provider with an injected fake (`renderWithRunContext` below — reusable by
//     any test for a component deep in App.tsx, including the breadcrumb pane);
//   - asserting an async success via the user-observable result (the copied
//     link + the "Link copied" accessible name), not by reaching into state;
//   - asserting the failure path renders a visible error affordance instead of
//     silently swallowing it.
//
// See docs/testing.md → "Frontend test layers" → "Rendering components that use
// context".
import { expect, test, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";

import { LinkButton, RunContext } from "./App.tsx";

// Render `ui` inside a real RunContext provider. Everything is a harmless no-op
// except the parts a test overrides — here, `createMessageLink`.
function renderWithRunContext(
  ui: ReactNode,
  overrides: Partial<React.ContextType<typeof RunContext>> = {},
) {
  const value: React.ContextType<typeof RunContext> = {
    openWorkspacePath: () => {},
    submitAnswer: async () => {},
    askUserQuestionDrafts: {},
    setAskUserQuestionDraft: () => {},
    createMessageLink: async (sessionId, entryId) =>
      `https://tank.example/sessions/${sessionId}?message=${entryId}`,
    user: null,
    ...overrides,
  };
  return render(
    <RunContext.Provider value={value}>{ui}</RunContext.Provider>,
  );
}

test("clicking copies the server-created deep link and announces success", async () => {
  const url = "https://tank.example/sessions/42?message=evt_7";
  const createMessageLink = vi.fn().mockResolvedValue(url);
  const user = userEvent.setup();

  renderWithRunContext(<LinkButton sessionId="42" entryId="evt_7" />, {
    createMessageLink,
  });

  await user.click(
    screen.getByRole("button", { name: "Copy link to message" }),
  );

  // The link is resolved server-side (durable cursor), not built in the browser.
  expect(createMessageLink).toHaveBeenCalledWith("42", "evt_7");
  // The resolved URL actually reached the clipboard...
  expect(await navigator.clipboard.readText()).toBe(url);
  // ...and the control announces success to assistive tech.
  expect(
    await screen.findByRole("button", { name: "Link copied" }),
  ).toBeInTheDocument();
});

test("a failed link creation surfaces a visible error, not a silent swallow", async () => {
  const createMessageLink = vi
    .fn()
    .mockRejectedValue(new Error("backend unavailable"));
  const user = userEvent.setup();

  renderWithRunContext(<LinkButton sessionId="42" entryId="evt_7" />, {
    createMessageLink,
  });

  await user.click(
    screen.getByRole("button", { name: "Copy link to message" }),
  );

  // The error path flips the tooltip so the failure is visible to the user.
  expect(
    await screen.findByTitle("Could not copy link"),
  ).toBeInTheDocument();
});
