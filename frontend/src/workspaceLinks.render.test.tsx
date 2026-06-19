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

test("renders workspace file markdown links without Streamdown blocked markers", () => {
  renderMarkdown(
    [
      "Please check [visual_verification_report.md](file:///workspace/chess-tactics/visual_verification_report.md) for full details.",
      "Keep [host file](file:///home/node/secret.txt) blocked.",
    ].join("\n"),
  );

  expect(screen.queryByText(/visual_verification_report\.md \[blocked\]/)).toBeNull();
  expect(
    screen.getByRole("link", { name: "visual_verification_report.md" }),
  ).toHaveAttribute(
    "href",
    "/workspace/chess-tactics/visual_verification_report.md",
  );
  expect(screen.getByText("host file [blocked]")).toBeInTheDocument();
});
