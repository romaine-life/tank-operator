import { describe, expect, it } from "vitest";

import { normalizeSessionPullRequests } from "./pullRequests";

describe("normalizeSessionPullRequests", () => {
  it("keeps complete refs verbatim and preserves order (server sorts)", () => {
    const got = normalizeSessionPullRequests([
      {
        repo: "romaine-life/tank-operator",
        number: 1360,
        url: "https://github.com/romaine-life/tank-operator/pull/1360",
        action: "github.pull_request.open",
        status: "succeeded",
        state: "clean",
        updated_at: "2026-06-19T00:00:00Z",
      },
      { url: "https://github.com/romaine-life/spirelens/pull/7" },
    ]);
    expect(got).toHaveLength(2);
    expect(got[0]).toEqual({
      repo: "romaine-life/tank-operator",
      number: 1360,
      url: "https://github.com/romaine-life/tank-operator/pull/1360",
      action: "github.pull_request.open",
      status: "succeeded",
      state: "clean",
      updated_at: "2026-06-19T00:00:00Z",
    });
    expect(got[1]).toEqual({
      url: "https://github.com/romaine-life/spirelens/pull/7",
    });
  });

  it("drops entries missing the load-bearing url and tolerates junk", () => {
    expect(normalizeSessionPullRequests([{ repo: "x", number: 1 }])).toEqual([]);
    expect(
      normalizeSessionPullRequests([null, 5, "x", { url: "" }, { url: 3 }]),
    ).toEqual([]);
  });

  it("returns [] for non-array / missing input ('no PR touched')", () => {
    expect(normalizeSessionPullRequests(undefined)).toEqual([]);
    expect(normalizeSessionPullRequests(null)).toEqual([]);
    expect(normalizeSessionPullRequests({})).toEqual([]);
    expect(normalizeSessionPullRequests("nope")).toEqual([]);
  });

  it("coerces a non-finite number to undefined", () => {
    const got = normalizeSessionPullRequests([
      { url: "https://github.com/o/r/pull/3", number: Number.NaN },
    ]);
    expect(got).toEqual([{ url: "https://github.com/o/r/pull/3" }]);
  });
});
