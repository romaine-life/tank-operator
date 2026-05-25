// RepoPicker is the splash-page chip-and-picker for selecting repos
// to auto-clone into a new session's /workspace.
//
// The picker owns durable repo selection, recent repo suggestions, and
// the chip+picker UI. The "All repos" enumeration is sourced from
// mcp-github; the session pod's repo-cloner init container consumes
// the selected repos at startup.
//
// UX shape:
//   - The selected repos render as removable chips above the trigger.
//   - A short row of recent repos stays visible on the splash page so
//     common choices are one click without opening the dialog.
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

import { isValidRepoSlug, recentRepoShortcutSlugs } from "../repos";

/** allRepos surfaces the user's full GitHub App installation, sourced
 *  from /api/github/repos. The picker filters this list by
 *  the typed input and renders matches above the Recent section.
 *  Optional — the picker still works on Recent + manual-entry when
 *  this prop isn't provided. */
export interface AllReposState {
  /** "idle" before the first fetch; "loading" during the fetch;
   *  "ready" when the list is populated; "error" when it failed. */
  status: "idle" | "loading" | "ready" | "error";
  /** The repo slugs the user's installation can see. Stable shape
   *  regardless of `status`; check `status` before rendering. */
  repos: string[];
  /** User-facing error string when status === "error". */
  error?: string | null;
}

export interface RepoPickerProps {
  /** Currently-staged repo slugs (chips). */
  selected: string[];
  /** Recently-used slugs surfaced as one-click suggestions. */
  recent: string[];
  /** Optional full-installation enumeration. */
  allRepos?: AllReposState;
  /** Called once when the picker opens so the parent can lazy-load
   *  /api/github/repos. Parent owns dedupe — picker calls this
   *  every time the open flag flips false → true. */
  onLoadAllRepos?: () => void;
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
  onSelectExclusive: (slug: string) => void;
  onRemove: (slug: string) => void;
}

const REPO_PICKER_MENU = "repo-picker";

export function RepoPicker(props: RepoPickerProps): JSX.Element {
  const {
    selected,
    recent,
    allRepos,
    onLoadAllRepos,
    open,
    input,
    error,
    busy,
    onToggleOpen,
    onClose,
    onInputChange,
    onAdd,
    onSelectExclusive,
    onRemove,
  } = props;

  // Lazy-load the All-repos enumeration on first open. Parent owns
  // dedupe (caches the response, replays on re-open after a successful
  // session create so just-cloned repos float in the list), the
  // picker just signals "user wants the data."
  const wasOpen = useRef(false);
  useEffect(() => {
    if (open && !wasOpen.current) {
      wasOpen.current = true;
      onLoadAllRepos?.();
    }
    if (!open) {
      wasOpen.current = false;
    }
  }, [open, onLoadAllRepos]);

  // Filter out already-selected (case-insensitive) so the "Recent"
  // section doesn't visually duplicate chips. Disabled-style retains
  // the slug in the section so the user sees "this was here, you
  // already added it" rather than the suggestion vanishing.
  const selectedLower = useRef<Set<string>>(new Set());
  selectedLower.current = new Set(selected.map((s) => s.toLowerCase()));

  const recentPreview = useMemo(() => recentRepoShortcutSlugs(recent), [recent]);

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
      {recentPreview.length > 0 && (
        <div className="home-repos-preview" aria-label="Recent repositories">
          <div className="home-repos-recent-label">Recent</div>
          <ul className="home-repos-recent-list" role="list">
            {recentPreview.map((slug, index) => {
              const selectedRecent = selectedLower.current.has(slug.toLowerCase());
              return (
                <li key={`preview:${slug}`} className="home-repos-recent-item">
                  <button
                    type="button"
                    className={
                      "home-repos-recent-chip home-repos-recent-shortcut" +
                      (selectedRecent ? " is-selected" : "")
                    }
                    onClick={() => onSelectExclusive(slug)}
                    disabled={busy}
                    title={slug}
                    aria-pressed={selectedRecent}
                    aria-keyshortcuts={String(index + 1)}
                    aria-label={`Select recent repository ${index + 1}: ${slug}`}
                  >
                    <span className="home-repos-recent-key" aria-hidden="true">
                      {index + 1}
                    </span>
                    <span>{slug}</span>
                  </button>
                </li>
              );
            })}
          </ul>
        </div>
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
            allRepos={allRepos}
            selectedLower={selectedLower.current}
            busy={busy}
            onAdd={onAdd}
          />
          <div className="home-repos-help">
            Selected repos clone into <code>/workspace</code> at session start.
          </div>
        </div>
      )}
    </section>
  );
}

// RepoPickerSuggestions renders the section below the manual-entry
// input. Two sources are shown in priority order:
//
//   1. Recent: the slugs the user has selected on prior sessions
//      (durable; sourced from GET /api/github/recent-repos).
//   2. All repos: the user's full GitHub App installation, sourced
//      from GET /api/github/repos. Lazy-loaded on first picker open.
//
// Both lists are live-filtered by the typed input (substring,
// case-insensitive). When the input is empty we hide All-repos
// behind a "all N installed" header so it doesn't dominate the panel;
// once the user starts typing, matching slugs from both sources are
// rendered as one-click chips. When neither source contributes a
// match, we show an honest "no matching repos" state with a hint to
// click Add for an exact slug.
interface RepoPickerSuggestionsProps {
  input: string;
  recent: string[];
  allRepos?: AllReposState;
  selectedLower: ReadonlySet<string>;
  busy: boolean;
  onAdd: (slug: string) => void;
}

function RepoPickerSuggestions(props: RepoPickerSuggestionsProps): JSX.Element {
  const { input, recent, allRepos, selectedLower, busy, onAdd } = props;
  const trimmed = input.trim();
  const looksLikeSlug = trimmed !== "" && isValidRepoSlug(trimmed);

  const needle = trimmed.toLowerCase();
  const matches = (slug: string) =>
    needle === "" || slug.toLowerCase().includes(needle);

  // Live-filter Recent by the trimmed input text. The match is
  // intentionally permissive (substring, case-insensitive) so a user
  // who has typed "nelsong6/" still sees their nelsong6 repos.
  const filteredRecent = useMemo(() => recent.filter(matches), [recent, trimmed]);

  // Filter All repos. When the user hasn't typed anything we show
  // the unfiltered list (capped visually by the CSS — overflow
  // scrolls). When they've typed, we narrow to substring matches.
  // Both lists exclude already-Recent slugs so the panel doesn't
  // render the same chip twice in different sections.
  const recentLower = useMemo(
    () => new Set(recent.map((s) => s.toLowerCase())),
    [recent],
  );
  const filteredAll = useMemo(() => {
    if (!allRepos || allRepos.status !== "ready") return [];
    return allRepos.repos.filter(
      (slug) => matches(slug) && !recentLower.has(slug.toLowerCase()),
    );
  }, [allRepos, trimmed, recentLower]);

  const totalShown = filteredRecent.length + filteredAll.length;
  const showAllRepos = allRepos !== undefined;

  // Empty-state messaging: surface why no suggestions appear, so the
  // picker doesn't read like a broken search box.
  if (totalShown === 0) {
    let body: string;
    if (allRepos?.status === "loading") {
      body = "Loading your GitHub installation…";
    } else if (allRepos?.status === "error") {
      body = `Couldn't load your repos: ${allRepos.error ?? "unknown error"}. Type owner/name and click Add to use it anyway.`;
    } else if (allRepos?.status === "ready" && allRepos.repos.length === 0) {
      body =
        "Your GitHub installation has no accessible repos. Type owner/name above and click Add to use an exact slug.";
    } else if (recent.length === 0 && !showAllRepos) {
      body = "No recent repos yet — type owner/name above and click Add.";
    } else {
      body = `No matching repos.${looksLikeSlug ? " Click Add to use this exact slug." : ""}`;
    }
    return <div className="home-repos-empty">{body}</div>;
  }

  return (
    <div className="home-repos-sections">
      {filteredRecent.length > 0 && (
        <RepoSection
          label="Recent"
          slugs={filteredRecent}
          filtered={trimmed !== ""}
          selectedLower={selectedLower}
          busy={busy}
          onAdd={onAdd}
        />
      )}
      {filteredAll.length > 0 && (
        <RepoSection
          label="All repos"
          slugs={filteredAll}
          filtered={trimmed !== ""}
          selectedLower={selectedLower}
          busy={busy}
          onAdd={onAdd}
        />
      )}
    </div>
  );
}

// RepoSection renders one labeled chip-list. Shared between Recent
// and All repos so the visual treatment stays in lockstep.
interface RepoSectionProps {
  label: string;
  slugs: string[];
  filtered: boolean;
  selectedLower: ReadonlySet<string>;
  busy: boolean;
  onAdd: (slug: string) => void;
}

function RepoSection(props: RepoSectionProps): JSX.Element {
  const { label, slugs, filtered, selectedLower, busy, onAdd } = props;
  return (
    <div className="home-repos-recent">
      <div className="home-repos-recent-label">
        {label}
        {filtered && (
          <span className="home-repos-recent-count">
            {" "}({slugs.length} match{slugs.length === 1 ? "" : "es"})
          </span>
        )}
      </div>
      <ul className="home-repos-recent-list" role="list">
        {slugs.map((slug) => {
          const alreadyPicked = selectedLower.has(slug.toLowerCase());
          return (
            <li key={`${label}:${slug}`} className="home-repos-recent-item">
              <button
                type="button"
                className={
                  "home-repos-recent-chip" + (alreadyPicked ? " is-disabled" : "")
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
