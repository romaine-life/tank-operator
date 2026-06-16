import { expect, test } from "vitest";
import { render, screen } from "@testing-library/react";
import { readFileSync } from "node:fs";

import { TokenUsageBadge } from "./App.tsx";

test("renders compact token usage with input output cached and reasoning details", () => {
  render(
    <TokenUsageBadge
      usage={{
        input_tokens: 10_000,
        input_tokens_details: { cached_tokens: 4_000 },
        output_tokens: 2_000,
        reasoning_output_tokens: 500,
        total_tokens: 12_500,
      }}
    />,
  );

  expect(screen.getByText("12k")).toBeInTheDocument();
  expect(screen.queryByText("tok")).not.toBeInTheDocument();
  expect(screen.getByLabelText(
    "Turn token usage: 12,500 total tokens · 10,000 input · 2,000 output · 4,000 cached · 500 reasoning",
  )).toBeInTheDocument();
});

test("tool token usage badge labels usage as turn scoped", () => {
  render(
    <TokenUsageBadge
      tone="tool"
      usage={{
        input_tokens: 120,
        output_tokens: 30,
        total_tokens: 150,
      }}
    />,
  );

  expect(screen.getByLabelText(
    "Current turn token usage; tools do not directly spend model tokens: 150 total tokens · 120 input · 30 output",
  )).toBeInTheDocument();
});

test("tool transcript rows do not render turn usage totals", () => {
  const source = readFileSync("src/App.tsx", "utf8");
  const runToolItem = source.match(
    /function RunToolItem\([\s\S]*?\nfunction toolItemDefaultExpanded/,
  )?.[0];

  expect(runToolItem).toBeTruthy();
  expect(runToolItem).not.toContain("<TokenUsageBadge");
});
