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
//   - Short Pinned and Recent groups stay visible on the splash page so
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
import { PinIcon } from "lucide-react";

import { isValidRepoSlug, isRepoPinned, repoShortcutSlugs } from "../repos";

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
  /** Locally-pinned repo slugs shown ahead of recent suggestions. */
  pinned: string[];
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
  onTogglePin: (slug: string) => void;
  onRemove: (slug: string) => void;
}

const REPO_PICKER_MENU = "repo-picker";

export function RepoPicker(props: RepoPickerProps): JSX.Element {
  const {
    selected,
    pinned,
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
    onTogglePin,
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

  const shortcutPreview = useMemo(
    () =>
      repoShortcutSlugs(pinned, recent).map((slug, index) => ({
        slug,
        shortcut: index + 1,
        pinned: isRepoPinned(pinned, slug),
      })),
    [pinned, recent],
  );
  const pinnedPreview = shortcutPreview.filter((item) => item.pinned);
  const recentPreview = shortcutPreview.filter((item) => !item.pinned);

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
              <PinToggleButton
                slug={slug}
                pinned={isRepoPinned(pinned, slug)}
                busy={busy}
                onTogglePin={onTogglePin}
              />
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
      {shortcutPreview.length > 0 && (
        <div className="home-repos-preview" aria-label="Pinned and recent repositories">
          {pinnedPreview.length > 0 && (
            <RepoPreviewSection
              label="Pinned"
              items={pinnedPreview}
              selectedLower={selectedLower.current}
              busy={busy}
              onSelectExclusive={onSelectExclusive}
              onTogglePin={onTogglePin}
            />
          )}
          {recentPreview.length > 0 && (
            <RepoPreviewSection
              label="Recent"
              items={recentPreview}
              selectedLower={selectedLower.current}
              busy={busy}
              onSelectExclusive={onSelectExclusive}
              onTogglePin={onTogglePin}
            />
          )}
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
            pinned={pinned}
            recent={recent}
            allRepos={allRepos}
            selectedLower={selectedLower.current}
            busy={busy}
            onAdd={onAdd}
            onTogglePin={onTogglePin}
          />
          <div className="home-repos-help">
            Selected repos clone into <code>/workspace</code> at session start.
          </div>
        </div>
      )}
    </section>
  );
}

interface RepoPreviewItem {
  slug: string;
  shortcut: number;
  pinned: boolean;
}

interface RepoPreviewSectionProps {
  label: string;
  items: RepoPreviewItem[];
  selectedLower: ReadonlySet<string>;
  busy: boolean;
  onSelectExclusive: (slug: string) => void;
  onTogglePin: (slug: string) => void;
}

function RepoPreviewSection(props: RepoPreviewSectionProps): JSX.Element {
  const { label, items, selectedLower, busy, onSelectExclusive, onTogglePin } = props;
  return (
    <div className="home-repos-preview-group">
      <div className="home-repos-recent-label">{label}</div>
      <ul className="home-repos-recent-list" role="list">
        {items.map((item) => {
          const selectedRecent = selectedLower.has(item.slug.toLowerCase());
          return (
            <li key={`preview:${item.slug}`} className="home-repos-recent-item">
              <button
                type="button"
                className={
                  "home-repos-recent-chip home-repos-recent-shortcut" +
                  (selectedRecent ? " is-selected" : "")
                }
                onClick={() => onSelectExclusive(item.slug)}
                disabled={busy}
                title={item.slug}
                aria-pressed={selectedRecent}
                aria-keyshortcuts={String(item.shortcut)}
                aria-label={`Select ${label.toLowerCase()} repository ${item.shortcut}: ${item.slug}`}
              >
                <span className="home-repos-recent-key" aria-hidden="true">
                  {item.shortcut}
                </span>
                <span>{item.slug}</span>
              </button>
              <PinToggleButton
                slug={item.slug}
                pinned={item.pinned}
                busy={busy}
                onTogglePin={onTogglePin}
              />
            </li>
          );
        })}
      </ul>
    </div>
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
  pinned: string[];
  recent: string[];
  allRepos?: AllReposState;
  selectedLower: ReadonlySet<string>;
  busy: boolean;
  onAdd: (slug: string) => void;
  onTogglePin: (slug: string) => void;
}

function RepoPickerSuggestions(props: RepoPickerSuggestionsProps): JSX.Element {
  const { input, pinned, recent, allRepos, selectedLower, busy, onAdd, onTogglePin } = props;
  const trimmed = input.trim();
  const looksLikeSlug = trimmed !== "" && isValidRepoSlug(trimmed);

  const needle = trimmed.toLowerCase();
  const matches = (slug: string) =>
    needle === "" || slug.toLowerCase().includes(needle);

  const filteredPinned = useMemo(() => pinned.filter(matches), [pinned, trimmed]);
  const pinnedLower = useMemo(
    () => new Set(pinned.map((s) => s.toLowerCase())),
    [pinned],
  );

  // Live-filter Recent by the trimmed input text. The match is
  // intentionally permissive (substring, case-insensitive) so a user
  // who has typed "romaine-life/" still sees their nelsong6 repos.
  const filteredRecent = useMemo(
    () =>
      recent.filter(
        (slug) => matches(slug) && !pinnedLower.has(slug.toLowerCase()),
      ),
    [recent, trimmed, pinnedLower],
  );

  // Filter All repos. When the user hasn't typed anything we show
  // the unfiltered list (capped visually by the CSS — overflow
  // scrolls). When they've typed, we narrow to substring matches.
  // The All list excludes already-pinned and already-recent slugs so
  // the panel doesn't render the same chip twice in different sections.
  const recentLower = useMemo(
    () => new Set(recent.map((s) => s.toLowerCase())),
    [recent],
  );
  const filteredAll = useMemo(() => {
    if (!allRepos || allRepos.status !== "ready") return [];
    return allRepos.repos.filter(
      (slug) =>
        matches(slug) &&
        !pinnedLower.has(slug.toLowerCase()) &&
        !recentLower.has(slug.toLowerCase()),
    );
  }, [allRepos, trimmed, pinnedLower, recentLower]);

  const totalShown = filteredPinned.length + filteredRecent.length + filteredAll.length;
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
      {filteredPinned.length > 0 && (
        <RepoSection
          label="Pinned"
          slugs={filteredPinned}
          filtered={trimmed !== ""}
          selectedLower={selectedLower}
          pinnedLower={pinnedLower}
          busy={busy}
          onAdd={onAdd}
          onTogglePin={onTogglePin}
        />
      )}
      {filteredRecent.length > 0 && (
        <RepoSection
          label="Recent"
          slugs={filteredRecent}
          filtered={trimmed !== ""}
          selectedLower={selectedLower}
          pinnedLower={pinnedLower}
          busy={busy}
          onAdd={onAdd}
          onTogglePin={onTogglePin}
        />
      )}
      {filteredAll.length > 0 && (
        <RepoSection
          label="All repos"
          slugs={filteredAll}
          filtered={trimmed !== ""}
          selectedLower={selectedLower}
          pinnedLower={pinnedLower}
          busy={busy}
          onAdd={onAdd}
          onTogglePin={onTogglePin}
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
  pinnedLower: ReadonlySet<string>;
  busy: boolean;
  onAdd: (slug: string) => void;
  onTogglePin: (slug: string) => void;
}

function RepoSection(props: RepoSectionProps): JSX.Element {
  const { label, slugs, filtered, selectedLower, pinnedLower, busy, onAdd, onTogglePin } = props;
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
          const pinnedRepo = pinnedLower.has(slug.toLowerCase());
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
              <PinToggleButton
                slug={slug}
                pinned={pinnedRepo}
                busy={busy}
                onTogglePin={onTogglePin}
              />
            </li>
          );
        })}
      </ul>
    </div>
  );
}

interface PinToggleButtonProps {
  slug: string;
  pinned: boolean;
  busy: boolean;
  onTogglePin: (slug: string) => void;
}

function PinToggleButton(props: PinToggleButtonProps): JSX.Element {
  const { slug, pinned, busy, onTogglePin } = props;
  return (
    <button
      type="button"
      className={"home-repos-pin" + (pinned ? " is-pinned" : "")}
      onClick={() => onTogglePin(slug)}
      disabled={busy}
      aria-pressed={pinned}
      aria-label={`${pinned ? "Unpin" : "Pin"} ${slug}`}
      title={`${pinned ? "Unpin" : "Pin"} ${slug}`}
    >
      <PinIcon aria-hidden="true" className="home-repos-pin-icon" />
    </button>
  );
}
