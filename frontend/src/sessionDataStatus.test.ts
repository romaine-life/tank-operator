import assert from "node:assert/strict";
import test from "node:test";
import { buildSessionDataStatusRows } from "./sessionDataStatus";

test("buildSessionDataStatusRows summarizes active workflow links", () => {
  const rows = buildSessionDataStatusRows({
    test_state: {
      active: true,
      slot_index: 3,
      url: "https://tank-operator-slot-3.tank.dev.romaine.life/",
      pull_request_url: "https://github.com/romaine-life/tank-operator/pull/123",
    },
    compaction_count: 2,
    runtime_context_window_tokens: 1_000_000,
    runtime_context_window_source: "provider",
    bug_label: { display_name: "bug: clipped status" },
    repos: ["romaine-life/tank-operator"],
    clone_state: {
      "romaine-life/tank-operator": { status: "completed" },
    },
  });

  assert.deepEqual(
    rows.map((row) => [row.id, row.status, row.tone]),
    [
      ["test", "Active", "good"],
      ["context", "Compacted", "warning"],
      ["rollout", "Inactive", "muted"],
      ["pull_request", "Linked", "info"],
      ["bug_report", "Linked", "info"],
      ["linked_repo", "Ready", "good"],
    ],
  );
  assert.equal(rows[0]?.detail, "Slot 3 reserved");
  assert.equal(rows[0]?.href, "https://tank-operator-slot-3.tank.dev.romaine.life/");
  assert.equal(rows[1]?.detail, "2 compactions / 1m window / provider");
  assert.equal(rows[3]?.detail, "romaine-life/tank-operator#123");
});

test("buildSessionDataStatusRows surfaces repo clone issues", () => {
  const rows = buildSessionDataStatusRows({
    repos: ["romaine-life/tank-operator", "romaine-life/glimmung"],
    clone_state: {
      "romaine-life/tank-operator": { status: "ready" },
      "romaine-life/glimmung": { phase: "failed", detail: "access denied" },
    },
  });

  const repo = rows.find((row) => row.id === "linked_repo");
  assert.equal(repo?.status, "Needs attention");
  assert.equal(repo?.tone, "danger");
  assert.equal(repo?.detail, "1/2 repo clone issue");
});

test("buildSessionDataStatusRows summarizes multiple bug labels", () => {
  const rows = buildSessionDataStatusRows({
    bug_labels: [
      { display_name: "bug: checkout" },
      { display_name: "bug: transcript" },
    ],
  });

  const bugReport = rows.find((row) => row.id === "bug_report");
  assert.equal(bugReport?.status, "2 linked");
  assert.equal(bugReport?.detail, "bug: checkout, bug: transcript");
  assert.equal(bugReport?.tone, "info");
});
