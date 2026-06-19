import { describe, expect, test } from "vitest";

import {
  buildProviderQuotaSnapshots,
  providerQuotaEvidenceFromPayload,
  providerQuotaSummary,
} from "./providerQuota";

describe("provider quota snapshots", () => {
  test("keeps primary and secondary Claude usage independent", () => {
    const observedAt = new Date().toISOString();
    const evidence = providerQuotaEvidenceFromPayload({
      rate_limits: [
        {
          provider: "anthropic",
          rateLimitType: "five_hour",
          utilization: 32,
          observedAt,
        },
        {
          provider: "anthropic",
          rateLimitType: "weekly",
          utilization: 34,
          observedAt,
        },
        {
          provider: "claude_secondary",
          rateLimitType: "five_hour",
          utilization: 41,
          observedAt,
        },
        {
          provider: "anthropic_secondary",
          rateLimitType: "weekly",
          utilization: 44,
          observedAt,
        },
      ],
    });

    const snapshots = buildProviderQuotaSnapshots([], evidence);

    expect(
      providerQuotaSummary(
        snapshots.anthropic.windows.find((window) => window.id === "five_hour"),
      ),
    ).toBe("68% left");
    expect(
      providerQuotaSummary(
        snapshots.anthropic.windows.find((window) => window.id === "weekly"),
      ),
    ).toBe("66% left");
    expect(
      providerQuotaSummary(
        snapshots.anthropic_secondary.windows.find(
          (window) => window.id === "five_hour",
        ),
      ),
    ).toBe("59% left");
    expect(
      providerQuotaSummary(
        snapshots.anthropic_secondary.windows.find(
          (window) => window.id === "weekly",
        ),
      ),
    ).toBe("56% left");
  });

  test("uses a secondary Claude session fallback when provider is absent", () => {
    const observedAt = new Date().toISOString();

    const snapshots = buildProviderQuotaSnapshots([
      {
        mode: "claude_secondary_gui",
        provider_rate_limit_observed_at: observedAt,
        provider_rate_limit_info: {
          rateLimitType: "five_hour",
          utilization: 25,
        },
      },
    ]);

    expect(
      providerQuotaSummary(
        snapshots.anthropic_secondary.windows.find(
          (window) => window.id === "five_hour",
        ),
      ),
    ).toBe("75% left");
    expect(
      providerQuotaSummary(
        snapshots.anthropic.windows.find((window) => window.id === "five_hour"),
      ),
    ).toBe("unknown");
  });
});
