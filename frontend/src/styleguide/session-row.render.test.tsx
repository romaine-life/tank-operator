import { afterEach, expect, test } from "vitest";
import { cleanup, render } from "@testing-library/react";

import { StyleguideSessionRow } from "./session-row";

afterEach(cleanup);

// These lock the DOM contract the nesting CSS hooks onto (index.css →
// ".sessions li.is-nested" / ".is-nested-last" / ".session-nest-connector").
// The styleguide deliberately mirrors the sidebar markup in App.tsx, so a
// rename here that drops a hook surfaces as a styleguide drift too. The
// ordering/clamping logic itself is covered by sessionTree.test.ts.

test("nested example rows carry the is-nested hook and a connector", () => {
  const { container } = render(<StyleguideSessionRow />);
  const nested = container.querySelectorAll("li.is-nested");
  expect(nested.length).toBe(2);
  for (const li of nested) {
    expect(
      li.querySelector(".session-nest-connector"),
      "every nested row needs the gutter connector element",
    ).not.toBeNull();
  }
});

test("exactly one nested row is the last child (the └─ elbow)", () => {
  const { container } = render(<StyleguideSessionRow />);
  expect(container.querySelectorAll("li.is-nested-last").length).toBe(1);
  // The elbow row is still a nested row.
  const last = container.querySelector("li.is-nested-last");
  expect(last?.classList.contains("is-nested")).toBe(true);
});

test("the connector is decorative (aria-hidden) and absent from root rows", () => {
  const { container } = render(<StyleguideSessionRow />);
  for (const connector of container.querySelectorAll(".session-nest-connector")) {
    expect(connector.getAttribute("aria-hidden")).toBe("true");
  }
  // Root rows (depth 0) never render a connector.
  for (const li of container.querySelectorAll('li[data-depth="0"]')) {
    expect(li.querySelector(".session-nest-connector")).toBeNull();
  }
});
