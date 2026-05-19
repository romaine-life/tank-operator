// RepoPicker is the splash-page chip-and-picker for selecting repos
// to auto-clone into a new session's /workspace.
//
// Stage 1 of the auto-clone feature (per docs/quality-timeframes.md
// "Acceptable chunking: a sequence of PRs where each PR leaves the
// system in a coherent state"). This PR ships the durable selection,
// the recent-repos surface, and the chip+picker UI. Stage 2 will add
// an "All repos" enumeration sourced from mcp-github; stage 3 ships
// the init container that actually clones.
//
// UX shape:
//   - The selected repos render as removable chips above the trigger.
//   - "+ Add repo" opens a small dropdown panel below the chip row.
//   - The panel has a text input ("owner/name") with an explicit Add
//     button, plus a "Recent" section of clickable suggestions
//     sourced from the user's prior sessions
//     (GET /api/github/recent-repos).
//   - Already-selected suggestions are visually dimmed and a no-op
//     to click — clearer than removing them from the list, which
//     would reorder things every time the user clicks a chip.
//
// The parent (App.tsx) owns all state — selected[], recent[], the
// open flag, the input value, and the inline validation error. This
// keeps the picker a pure render of the parent's state, which is
// what the vitest in RepoPicker.test.tsx asserts.

import { useCallback, useEffect, useMemo, useRef } from "react";

import { isValidRepoSlug } from "../repos";

export interface RepoPickerProps {
  /** Currently-staged repo slugs (chips). */
  selected: string[];
  /** Recently-used slugs surfaced as one-click suggestions. */
  recent: string[];
  /** Whether the picker panel is open. */
  open: boolean;
  /** Current value of the manual-entry text input. */
  input: string;
  /** Inline validation error, or null. */
  error: string | null;
  /** When true, all interactive controls are disabled. */
  busy: boolean;

  onToggleOpen: () => void;
  onClose: () => void;
  onInputChange: (value: string) => void;
  onAdd: (slug: string) => void;
  onRemove: (slug: string) => void;
}

const REPO_PICKER_MENU = "repo-picker";

export function RepoPicker(props: RepoPickerProps): JSX.Element {
  const {
    selected,
    recent,
    open,
    input,
    error,
    busy,
    onToggleOpen,
    onClose,
    onInputChange,
    onAdd,
    onRemove,
  } = props;

  // Filter out already-selected (case-insensitive) so the "Recent"
  // section doesn't visually duplicate chips. Disabled-style retains
  // the slug in the section so the user sees "this was here, you
  // already added it" rather than the suggestion vanishing.
  const selectedLower = useRef<Set<string>>(new Set());
  selectedLower.current = new Set(selected.map((s) => s.toLowerCase()));

  // Close on Escape or outside-click — matches the profile menu shape
  // used elsewhere in App.tsx (data-menu attribute on the root).
  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    const onMouseDown = (e: MouseEvent) => {
      const target = e.target as HTMLElement | null;
      const root = target?.closest("[data-menu]") as HTMLElement | null;
      if (root?.dataset.menu === REPO_PICKER_MENU) return;
      onClose();
    };
    document.addEventListener("keydown", onKey);
    document.addEventListener("mousedown", onMouseDown);
    return () => {
      document.removeEventListener("keydown", onKey);
      document.removeEventListener("mousedown", onMouseDown);
    };
  }, [open, onClose]);

  const handleSubmit = useCallback(
    (e: React.FormEvent<HTMLFormElement>) => {
      e.preventDefault();
      onAdd(input);
    },
    [input, onAdd],
  );

  return (
    <section className="home-repos" data-menu={REPO_PICKER_MENU} aria-label="Repositories">
      <div className="home-repos-head">
        <h3 className="home-repos-title">Repositories</h3>
        <span className="home-repos-meta">
          {selected.length === 0 ? "none selected" : `${selected.length} selected`}
        </span>
      </div>
      {selected.length > 0 && (
        <ul className="home-repos-chips" role="list">
          {selected.map((slug) => (
            <li key={slug} className="home-repos-chip">
              <span className="home-repos-chip-slug">{slug}</span>
              <button
                type="button"
                className="home-repos-chip-remove"
                onClick={() => onRemove(slug)}
                disabled={busy}
                aria-label={`Remove ${slug}`}
                title={`Remove ${slug}`}
              >
                ×
              </button>
            </li>
          ))}
        </ul>
      )}
      <button
        type="button"
        className="home-repos-trigger"
        onClick={onToggleOpen}
        aria-expanded={open}
        aria-haspopup="dialog"
        disabled={busy}
      >
        {open ? "Close" : "+ Add repo"}
      </button>
      {open && (
        <div className="home-repos-panel" role="dialog" aria-label="Add repository">
          <form className="home-repos-form" onSubmit={handleSubmit}>
            <input
              className="home-repos-input"
              type="text"
              placeholder="owner/name"
              value={input}
              autoFocus
              onChange={(e) => onInputChange(e.target.value)}
              disabled={busy}
              aria-label="Repository slug"
            />
            <button
              type="submit"
              className="home-repos-add"
              disabled={busy || input.trim().length === 0}
            >
              Add
            </button>
          </form>
          {error && <div className="home-repos-error" role="alert">{error}</div>}
          <RepoPickerSuggestions
            input={input}
            recent={recent}
            selectedLower={selectedLower.current}
            busy={busy}
            onAdd={onAdd}
          />
          <div className="home-repos-help">
            Selected repos clone into <code>/workspace</code> at session start.
            Live search across your GitHub installation lands in stage 2.
          </div>
        </div>
      )}
    </section>
  );
}

// RepoPickerSuggestions renders the section below the manual-entry
// input. Stage 1 has no GitHub-side search, so this block is
// deliberately honest about that:
//
//   - When the user has typed a valid `owner/name`, we point them at
//     the Add button.
//   - When the user has typed anything else, we show a "no live
//     search yet" hint with the regex shape.
//   - The Recent list is filtered by the input text (substring,
//     case-insensitive) so the user gets the closest thing to
//     autocomplete we can offer from data we have.
//   - When Recent is empty AND no input, we show an explicit empty
//     state so the picker doesn't look like a broken search box.
//
// Stage 2 will replace this block with the mcp-github "All repos"
// lookahead.
interface RepoPickerSuggestionsProps {
  input: string;
  recent: string[];
  selectedLower: ReadonlySet<string>;
  busy: boolean;
  onAdd: (slug: string) => void;
}

function RepoPickerSuggestions(props: RepoPickerSuggestionsProps): JSX.Element {
  const { input, recent, selectedLower, busy, onAdd } = props;
  const trimmed = input.trim();
  const looksLikeSlug = trimmed !== "" && isValidRepoSlug(trimmed);

  // Live-filter Recent by the trimmed input text. The match is
  // intentionally permissive (substring, case-insensitive) so a user
  // who has typed "nelsong6/" still sees their nelsong6 repos.
  const filteredRecent = useMemo(() => {
    if (recent.length === 0) return [];
    if (trimmed === "") return recent;
    const needle = trimmed.toLowerCase();
    return recent.filter((slug) => slug.toLowerCase().includes(needle));
  }, [recent, trimmed]);

  // Empty-state messaging: surface why no suggestions appear, so the
  // picker doesn't read like a broken search box. Three states:
  //   1. Recent is empty (first-time user) — "No recent repos yet"
  //   2. Recent has items but the typed input filters them all out — "No matching recent repos"
  //   3. Recent has items and at least one matches — render the list
  if (filteredRecent.length === 0) {
    return (
      <div className="home-repos-empty">
        {recent.length === 0
          ? "No recent repos yet — type owner/name above and click Add."
          : `No matching recent repos.${looksLikeSlug ? " Click Add to use this exact slug." : ""}`}
      </div>
    );
  }

  return (
    <div className="home-repos-recent">
      <div className="home-repos-recent-label">
        Recent
        {trimmed !== "" && (
          <span className="home-repos-recent-count">
            {" "}({filteredRecent.length} match{filteredRecent.length === 1 ? "" : "es"})
          </span>
        )}
      </div>
      <ul className="home-repos-recent-list" role="list">
        {filteredRecent.map((slug) => {
          const alreadyPicked = selectedLower.has(slug.toLowerCase());
          return (
            <li key={slug} className="home-repos-recent-item">
              <button
                type="button"
                className={
                  "home-repos-recent-chip" +
                  (alreadyPicked ? " is-disabled" : "")
                }
                onClick={() => !alreadyPicked && onAdd(slug)}
                disabled={busy || alreadyPicked}
                title={alreadyPicked ? `${slug} (already added)` : slug}
              >
                {slug}
              </button>
            </li>
          );
        })}
      </ul>
    </div>
  );
}
