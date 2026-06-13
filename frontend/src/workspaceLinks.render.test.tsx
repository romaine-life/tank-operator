// Render test for the exact markdown boundary that turns model text into DOM.
// The pure workspaceLinks tests pin parser output; this layer proves Streamdown
// no longer exposes a `[blocked]` marker for workspace file links.
import { expect, test } from "vitest";
import { render, screen } from "@testing-library/react";
import { Streamdown } from "streamdown";

import { linkTextTargetsInMarkdown } from "./workspaceLinks.ts";

function renderMarkdown(markdown: string) {
  return render(
    <Streamdown linkSafety={{ enabled: false }}>
      {linkTextTargetsInMarkdown(markdown)}
    </Streamdown>,
  );
}

test("renders browsable file links, leaves secret-path links blocked", () => {
  renderMarkdown(
    [
      "Please check [visual_verification_report.md](file:///workspace/chess-tactics/visual_verification_report.md) for full details.",
      "Open [the plan](file:///home/node/.claude/plan.md) too.",
      "Keep [token](file:///var/run/secrets/auth.romaine.life/token) blocked.",
    ].join("\n"),
  );

  expect(screen.queryByText(/visual_verification_report\.md \[blocked\]/)).toBeNull();
  expect(
    screen.getByRole("link", { name: "visual_verification_report.md" }),
  ).toHaveAttribute(
    "href",
    "/workspace/chess-tactics/visual_verification_report.md",
  );
  // ~/.claude is a browsable root now → its file link renders as a real link.
  expect(screen.getByRole("link", { name: "the plan" })).toHaveAttribute(
    "href",
    "/home/node/.claude/plan.md",
  );
  // A secret-mount path is never linkified, so Streamdown keeps it blocked.
  expect(screen.getByText("token [blocked]")).toBeInTheDocument();
});
