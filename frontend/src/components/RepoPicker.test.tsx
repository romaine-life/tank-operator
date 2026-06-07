// Component/interaction test (jsdom project — `.test.tsx`) for the splash repo
// picker's selection model. The behavior under test:
//
//   - Clicking a repo name selects it EXCLUSIVELY (the staged set becomes just
//     that repo) — onSelect(slug, "exclusive").
//   - Shift-clicking a repo name is ADDITIVE (joins the current selection) —
//     onSelect(slug, "additive").
//   - The explicit "+" affordance on each chip is ADDITIVE without Shift.
//   - An already-staged repo stays selectable (a plain click narrows the
//     selection back to just it) while its "+" is disabled, because additive-
//     adding a repo already in the set is a no-op.
//
// The exclusive/additive decision itself lives in repos.ts `applyRepoSelection`
// (unit-tested in repos.test.ts); this suite proves the picker maps the click
// gesture to the right mode. The number-shortcut half of the same contract is
// wired in App.tsx and shares applyRepoSelection.
import { expect, test, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";

import { RepoPicker, type RepoPickerProps } from "./RepoPicker";

function setup(overrides: Partial<RepoPickerProps> = {}) {
  const onSelect = vi.fn();
  const onAdd = vi.fn();
  const props: RepoPickerProps = {
    selected: [],
    pinned: [],
    recent: [],
    open: false,
    input: "",
    error: null,
    busy: false,
    onToggleOpen: vi.fn(),
    onClose: vi.fn(),
    onInputChange: vi.fn(),
    onAdd,
    onSelect,
    onTogglePin: vi.fn(),
    onReorderPin: vi.fn(),
    onRemove: vi.fn(),
    onDismissRecent: vi.fn(),
    ...overrides,
  };
  render(<RepoPicker {...props} />);
  return { onSelect, onAdd };
}

test("a plain click on a repo shortcut selects it exclusively", () => {
  const { onSelect } = setup({ pinned: ["romaine-life/tank-operator"] });

  fireEvent.click(
    screen.getByRole("button", {
      name: /Select pinned repository 1: romaine-life\/tank-operator/,
    }),
  );

  expect(onSelect).toHaveBeenCalledTimes(1);
  expect(onSelect).toHaveBeenCalledWith("romaine-life/tank-operator", "exclusive");
});

test("a Shift-click on a repo shortcut is additive", () => {
  const { onSelect } = setup({ pinned: ["romaine-life/tank-operator"] });

  fireEvent.click(
    screen.getByRole("button", {
      name: /Select pinned repository 1: romaine-life\/tank-operator/,
    }),
    { shiftKey: true },
  );

  expect(onSelect).toHaveBeenCalledTimes(1);
  expect(onSelect).toHaveBeenCalledWith("romaine-life/tank-operator", "additive");
});

test("the + affordance adds a repo additively without Shift", () => {
  const { onSelect } = setup({ recent: ["romaine-life/glimmung"] });

  fireEvent.click(
    screen.getByRole("button", { name: "Add romaine-life/glimmung to selection" }),
  );

  expect(onSelect).toHaveBeenCalledTimes(1);
  expect(onSelect).toHaveBeenCalledWith("romaine-life/glimmung", "additive");
});

test("an already-staged shortcut stays selectable; its + is disabled", () => {
  const { onSelect } = setup({
    pinned: ["romaine-life/tank-operator"],
    selected: ["romaine-life/tank-operator"],
  });

  const chip = screen.getByRole("button", {
    name: /Select pinned repository 1: romaine-life\/tank-operator/,
  });
  // Staged repos read as selected but remain clickable: a plain click narrows
  // the selection back down to just this repo.
  expect(chip).toBeEnabled();
  expect(chip).toHaveAttribute("aria-pressed", "true");
  fireEvent.click(chip);
  expect(onSelect).toHaveBeenCalledWith("romaine-life/tank-operator", "exclusive");

  // The "+" is disabled because additive-adding a staged repo is a no-op.
  expect(
    screen.getByRole("button", {
      name: "romaine-life/tank-operator already selected",
    }),
  ).toBeDisabled();
});

test("panel suggestion chips follow the same exclusive/additive model", () => {
  const { onSelect } = setup({
    open: true,
    allRepos: {
      status: "ready",
      repos: ["octocat/hello", "romaine-life/tank-operator"],
    },
    selected: ["romaine-life/tank-operator"],
  });

  // An unstaged "All repos" chip selects exclusively on a plain click...
  fireEvent.click(screen.getByRole("button", { name: "octocat/hello" }));
  expect(onSelect).toHaveBeenLastCalledWith("octocat/hello", "exclusive");

  // ...and additively via its "+".
  fireEvent.click(
    screen.getByRole("button", { name: "Add octocat/hello to selection" }),
  );
  expect(onSelect).toHaveBeenLastCalledWith("octocat/hello", "additive");

  // A staged repo in the panel is now selectable (previously it was a disabled
  // no-op), so a plain click narrows the selection to just it.
  const stagedChip = screen.getByRole("button", {
    name: "romaine-life/tank-operator",
  });
  expect(stagedChip).toBeEnabled();
  fireEvent.click(stagedChip);
  expect(onSelect).toHaveBeenLastCalledWith("romaine-life/tank-operator", "exclusive");
});

test("the manual typed-entry Add stays a distinct additive action", () => {
  // The form's Add button routes through onAdd (addRepoSlug semantics), not the
  // exclusive/additive onSelect gesture — typing a slug should never wipe the
  // existing selection.
  const { onAdd, onSelect } = setup({ open: true, input: "octocat/hello" });

  fireEvent.click(screen.getByRole("button", { name: "Add" }));

  expect(onAdd).toHaveBeenCalledWith("octocat/hello");
  expect(onSelect).not.toHaveBeenCalled();
});

test("submitting the form selects the best match (pinned > recent > all)", () => {
  // Setup with pinned, recent, and all repos, and input that matches all,
  // to verify it selects the best match (pinned in this case).
  const { onAdd } = setup({
    open: true,
    input: "hello",
    pinned: ["org/hello-pinned"],
    recent: ["org/hello-recent"],
    allRepos: {
      status: "ready",
      repos: ["org/hello-all"],
    },
  });

  const form = screen.getByRole("dialog").querySelector("form");
  expect(form).not.toBeNull();
  fireEvent.submit(form!);

  expect(onAdd).toHaveBeenCalledWith("org/hello-pinned");
});

test("submitting the form selects recent if no pinned matches", () => {
  const { onAdd } = setup({
    open: true,
    input: "hello",
    pinned: ["org/other-pinned"],
    recent: ["org/hello-recent"],
    allRepos: {
      status: "ready",
      repos: ["org/hello-all"],
    },
  });

  const form = screen.getByRole("dialog").querySelector("form");
  expect(form).not.toBeNull();
  fireEvent.submit(form!);

  expect(onAdd).toHaveBeenCalledWith("org/hello-recent");
});

test("submitting the form selects all repos if no pinned or recent matches", () => {
  const { onAdd } = setup({
    open: true,
    input: "hello",
    pinned: ["org/other-pinned"],
    recent: ["org/other-recent"],
    allRepos: {
      status: "ready",
      repos: ["org/hello-all"],
    },
  });

  const form = screen.getByRole("dialog").querySelector("form");
  expect(form).not.toBeNull();
  fireEvent.submit(form!);

  expect(onAdd).toHaveBeenCalledWith("org/hello-all");
});

test("submitting the form falls back to typed input if no match exists", () => {
  const { onAdd } = setup({
    open: true,
    input: "hello-world",
    pinned: ["org/other-pinned"],
    recent: ["org/other-recent"],
    allRepos: {
      status: "ready",
      repos: ["org/other-all"],
    },
  });

  const form = screen.getByRole("dialog").querySelector("form");
  expect(form).not.toBeNull();
  fireEvent.submit(form!);

  expect(onAdd).toHaveBeenCalledWith("hello-world");
});

