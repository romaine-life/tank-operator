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
//   - Selection is a single-select-by-default, multi-select-on-intent
//     model, matching how OS file lists behave. Clicking a repo name (or
//     pressing its number shortcut) selects it EXCLUSIVELY — the staged
//     set becomes exactly that repo. Shift-clicking (or Shift+number), and
//     the explicit "+" affordance on each chip, are ADDITIVE — the repo
//     joins the current selection instead of replacing it. The split lives
//     in repos.ts `applyRepoSelection`; the picker only maps the gesture.
//   - An already-staged repo stays clickable: a plain click narrows the
//     selection back down to just it, while its "+" is disabled because
//     additive-adding a repo already in the set is a no-op. Staged repos
//     read as selected (accent fill + aria-pressed), not dimmed-and-dead.
//   - "+ Add repo" opens a small dropdown panel below the chip row.
//   - The panel has a text input ("owner/name") with an explicit Add
//     button, plus a "Recent" section of clickable suggestions
//     sourced from the user's prior sessions
//     (GET /api/github/recent-repos). The typed-entry Add button stays
//     additive-with-validation (addRepoSlug) so a duplicate you typed is
//     surfaced as an error rather than silently swallowed.
//   - Pin order is user-controlled by drag-and-drop on two surfaces:
//     the always-visible numbered "Pinned" shortcuts (drag a chip) and
//     the full "Pinned" list inside the panel (drag a grip handle, or
//     use ArrowUp/ArrowDown for keyboard reorder). Both share one drag
//     implementation (usePinDragReorder). The durable pinned_repos[]
//     order IS the pin order and the shortcut order, so every reorder
//     is the same PUT the pin toggle uses — the parent owns the
//     optimistic-then-reconciled write.
//
// The parent (App.tsx) owns all state — selected[], recent[], the
// open flag, the input value, and the inline validation error. This
// keeps the picker a pure render of the parent's state, which is
// what the vitest in RepoPicker.test.tsx asserts.

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { GripVertical, PinIcon, PlusIcon, XIcon } from "lucide-react";

import {
  isValidRepoSlug,
  isRepoPinned,
  repoShortcutSlugs,
  type RepoSelectionMode,
} from "../repos";

// repoSelectModeFromEvent maps a chip click to its selection gesture: a plain
// left click selects the repo exclusively (it becomes the only staged repo),
// while holding Shift makes the click additive (the repo joins the current
// selection). The keyboard number shortcuts in App.tsx use the same rule —
// bare number = exclusive, Shift+number = additive — so pointer and keyboard
// stay in lockstep. The explicit "+" affordance is always additive regardless
// of modifiers.
function repoSelectModeFromEvent(
  e: { shiftKey: boolean },
): RepoSelectionMode {
  return e.shiftKey ? "additive" : "exclusive";
}

/** onSelect picks a repo via a chip/shortcut gesture: exclusive (plain) or
 *  additive (Shift / the "+" affordance). Distinct from onAdd, which is the
 *  manual typed-entry Add button. */
type RepoSelectHandler = (slug: string, mode: RepoSelectionMode) => void;

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
  /** Manual typed-entry Add (the panel form). Additive with validation —
   *  duplicates surface as an inline error via addRepoSlug. */
  onAdd: (slug: string) => void;
  /** Chip/shortcut selection gesture. `mode` is "exclusive" for a plain
   *  click / bare number and "additive" for Shift / the "+" affordance. */
  onSelect: RepoSelectHandler;
  onTogglePin: (slug: string) => void;
  /** Reorder the durable pin list by moving `sourceSlug` relative to
   *  `targetSlug`. Wired to the same PUT /api/github/pinned-repos write
   *  as pin/unpin — the pinned_repos[] order is the pin order. */
  onReorderPin: (sourceSlug: string, targetSlug: string) => void;
  onRemove: (slug: string) => void;
  onDismissRecent: (slug: string) => void;
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
    onSelect,
    onTogglePin,
    onReorderPin,
    onRemove,
    onDismissRecent,
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

  // Lower-cased staged set, used to mark suggestion/shortcut chips as selected
  // (case-insensitive). A staged repo is NOT removed from its section: it stays
  // visible and clickable (a plain click narrows the selection back to just
  // it), reads as selected via `is-selected` + aria-pressed, and only its "+"
  // additive control is disabled — additive-adding a staged repo is a no-op.
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
              onSelect={onSelect}
              onTogglePin={onTogglePin}
              onReorderPin={onReorderPin}
            />
          )}
          {recentPreview.length > 0 && (
            <RepoPreviewSection
              label="Recent"
              items={recentPreview}
              selectedLower={selectedLower.current}
              busy={busy}
              onSelect={onSelect}
              onTogglePin={onTogglePin}
              onDismissRecent={onDismissRecent}
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
            onSelect={onSelect}
            onTogglePin={onTogglePin}
            onReorderPin={onReorderPin}
            onDismissRecent={onDismissRecent}
          />
          <div className="home-repos-help">
            Selected repos clone into <code>/workspace</code> at session start.
          </div>
        </div>
      )}
    </section>
  );
}

// NOOP_REORDER is a stable no-op so usePinDragReorder can be called
// unconditionally (rules of hooks) from a section that isn't reorderable.
const NOOP_REORDER = (): void => {};

// usePinDragReorder is the shared HTML5 drag-and-drop reorder behavior for the
// pinned-repo surfaces: the always-visible shortcut preview and the picker
// panel's full Pinned list. Both reorder the same durable list, so they share
// one drag implementation — `dragHandlers(slug)` spreads onto a list item and
// `itemState(slug)` drives the dragging/drop-target styling. The actual reorder
// is slug-based (`onReorderPin(source, target)`), so dragging works the same
// whether the surface shows the full list or just the first few pins.
function usePinDragReorder(
  busy: boolean,
  onReorderPin: (sourceSlug: string, targetSlug: string) => void,
) {
  const [draggingSlug, setDraggingSlug] = useState<string | null>(null);
  const [overSlug, setOverSlug] = useState<string | null>(null);

  const clearDrag = useCallback(() => {
    setDraggingSlug(null);
    setOverSlug(null);
  }, []);

  const itemState = (slug: string) => {
    const key = slug.toLowerCase();
    const isDragging = draggingSlug?.toLowerCase() === key;
    const isDropTarget =
      !!draggingSlug && !isDragging && overSlug?.toLowerCase() === key;
    return { isDragging, isDropTarget };
  };

  const dragHandlers = (slug: string) => ({
    draggable: !busy,
    onDragStart: (e: React.DragEvent) => {
      if (busy) return;
      setDraggingSlug(slug);
      setOverSlug(slug);
      e.dataTransfer.effectAllowed = "move";
      // Firefox requires data to be set for a drag to start.
      e.dataTransfer.setData("text/plain", slug);
    },
    onDragEnter: () => {
      if (draggingSlug) setOverSlug(slug);
    },
    onDragOver: (e: React.DragEvent) => {
      if (!draggingSlug) return;
      e.preventDefault();
      e.dataTransfer.dropEffect = "move";
      if (overSlug?.toLowerCase() !== slug.toLowerCase()) setOverSlug(slug);
    },
    onDrop: (e: React.DragEvent) => {
      e.preventDefault();
      if (draggingSlug && draggingSlug.toLowerCase() !== slug.toLowerCase()) {
        onReorderPin(draggingSlug, slug);
      }
      clearDrag();
    },
    onDragEnd: clearDrag,
  });

  return { itemState, dragHandlers };
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
  onSelect: RepoSelectHandler;
  onTogglePin: (slug: string) => void;
  /** When provided, the section's chips become drag-to-reorder — used for the
   *  always-visible Pinned shortcuts so users can rearrange pin order without
   *  opening the picker. Reorder runs through the same durable PUT as the panel
   *  Pinned list; Recent chips never receive this (recency is not user-ordered). */
  onReorderPin?: (sourceSlug: string, targetSlug: string) => void;
  onDismissRecent?: (slug: string) => void;
}

function RepoPreviewSection(props: RepoPreviewSectionProps): JSX.Element {
  const { label, items, selectedLower, busy, onSelect, onTogglePin, onReorderPin, onDismissRecent } =
    props;
  // Called unconditionally (rules of hooks); a section is reorderable only when
  // a reorder callback is supplied and there is more than one chip to order.
  const { itemState, dragHandlers } = usePinDragReorder(busy, onReorderPin ?? NOOP_REORDER);
  const reorderable = !!onReorderPin && items.length > 1;
  return (
    <div className="home-repos-preview-group">
      <div className="home-repos-recent-label">
        {label}
        {reorderable && (
          <span className="home-repos-recent-count"> (drag to reorder)</span>
        )}
      </div>
      <ul className="home-repos-recent-list" role="list">
        {items.map((item) => {
          const selectedRecent = selectedLower.has(item.slug.toLowerCase());
          const { isDragging, isDropTarget } = reorderable
            ? itemState(item.slug)
            : { isDragging: false, isDropTarget: false };
          const dnd = reorderable ? dragHandlers(item.slug) : {};
          const itemClass =
            "home-repos-recent-item" +
            (reorderable ? " home-repos-reorderable-chip" : "") +
            (isDragging ? " is-dragging" : "") +
            (isDropTarget ? " is-drop-target" : "");
          return (
            <li key={`preview:${item.slug}`} className={itemClass} {...dnd}>
              <button
                type="button"
                className={
                  "home-repos-recent-chip home-repos-recent-shortcut" +
                  (selectedRecent ? " is-selected" : "")
                }
                onClick={(e) => onSelect(item.slug, repoSelectModeFromEvent(e))}
                disabled={busy}
                title={
                  reorderable
                    ? `${item.slug} — click to select, Shift-click to add, drag to reorder`
                    : `${item.slug} — click to select, Shift-click to add`
                }
                aria-pressed={selectedRecent}
                aria-keyshortcuts={`${item.shortcut} Shift+${item.shortcut}`}
                aria-label={`Select ${label.toLowerCase()} repository ${item.shortcut}: ${item.slug} (Shift to add)`}
              >
                <span className="home-repos-recent-key" aria-hidden="true">
                  {item.shortcut}
                </span>
                <span>{item.slug}</span>
              </button>
              <AdditiveAddButton
                slug={item.slug}
                selected={selectedRecent}
                busy={busy}
                onSelect={onSelect}
              />
              {!item.pinned && onDismissRecent && (
                <button
                  type="button"
                  className="home-repos-recent-remove"
                  onClick={() => onDismissRecent(item.slug)}
                  disabled={busy}
                  aria-label={`Remove ${item.slug} from recent repositories`}
                  title={`Remove ${item.slug} from Recent`}
                >
                  <XIcon aria-hidden="true" className="home-repos-recent-remove-icon" />
                </button>
              )}
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
  onSelect: RepoSelectHandler;
  onTogglePin: (slug: string) => void;
  onReorderPin: (sourceSlug: string, targetSlug: string) => void;
  onDismissRecent: (slug: string) => void;
}

function RepoPickerSuggestions(props: RepoPickerSuggestionsProps): JSX.Element {
  const { input, pinned, recent, allRepos, selectedLower, busy, onSelect, onTogglePin, onReorderPin, onDismissRecent } = props;
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
      {filteredPinned.length > 0 &&
        (trimmed === "" ? (
          // Unfiltered: the Pinned section lists the full pin set in its
          // durable order, so it is the canonical reorder surface — drag a
          // grip handle (or use the arrow keys) to rearrange. A search filter
          // shows only a subset, where "reorder" has no well-defined meaning,
          // so the filtered branch falls back to the static RepoSection.
          <DraggablePinnedSection
            slugs={filteredPinned}
            selectedLower={selectedLower}
            busy={busy}
            onSelect={onSelect}
            onTogglePin={onTogglePin}
            onReorderPin={onReorderPin}
          />
        ) : (
          <RepoSection
            label="Pinned"
            slugs={filteredPinned}
            filtered
            selectedLower={selectedLower}
            pinnedLower={pinnedLower}
            busy={busy}
            onSelect={onSelect}
            onTogglePin={onTogglePin}
          />
        ))}
      {filteredRecent.length > 0 && (
        <RepoSection
          label="Recent"
          slugs={filteredRecent}
          filtered={trimmed !== ""}
          selectedLower={selectedLower}
          pinnedLower={pinnedLower}
          busy={busy}
          onSelect={onSelect}
          onTogglePin={onTogglePin}
          onDismissRecent={onDismissRecent}
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
          onSelect={onSelect}
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
  onSelect: RepoSelectHandler;
  onTogglePin: (slug: string) => void;
  onDismissRecent?: (slug: string) => void;
}

function RepoSection(props: RepoSectionProps): JSX.Element {
  const { label, slugs, filtered, selectedLower, pinnedLower, busy, onSelect, onTogglePin, onDismissRecent } = props;
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
                  "home-repos-recent-chip" + (alreadyPicked ? " is-selected" : "")
                }
                onClick={(e) => onSelect(slug, repoSelectModeFromEvent(e))}
                disabled={busy}
                aria-pressed={alreadyPicked}
                title={
                  alreadyPicked
                    ? `${slug} (selected) — click to keep only this, Shift-click to add`
                    : `${slug} — click to select, Shift-click to add`
                }
              >
                {slug}
              </button>
              <AdditiveAddButton
                slug={slug}
                selected={alreadyPicked}
                busy={busy}
                onSelect={onSelect}
              />
              <PinToggleButton
                slug={slug}
                pinned={pinnedRepo}
                busy={busy}
                onTogglePin={onTogglePin}
              />
              {label === "Recent" && onDismissRecent && (
                <button
                  type="button"
                  className="home-repos-recent-remove"
                  onClick={() => onDismissRecent(slug)}
                  disabled={busy}
                  aria-label={`Remove ${slug} from recent repositories`}
                  title={`Remove ${slug} from Recent`}
                >
                  <XIcon aria-hidden="true" className="home-repos-recent-remove-icon" />
                </button>
              )}
            </li>
          );
        })}
      </ul>
    </div>
  );
}

// DraggablePinnedSection renders the unfiltered "Pinned" list as a
// drag-and-drop / keyboard reorderable list. It is the canonical surface for
// rearranging pins: the durable pinned_repos[] order is the pin order (and the
// splash shortcut order), so dropping or arrow-key-moving a row issues a
// reorder through the same PUT the pin toggle uses. The grip handle is the
// drag origin and an accessible reorder control (ArrowUp/ArrowDown), so pointer
// and keyboard users get the same capability. Filtered/partial pin lists fall
// back to the static RepoSection — reordering a subset has no clear meaning.
interface DraggablePinnedSectionProps {
  slugs: string[];
  selectedLower: ReadonlySet<string>;
  busy: boolean;
  onSelect: RepoSelectHandler;
  onTogglePin: (slug: string) => void;
  onReorderPin: (sourceSlug: string, targetSlug: string) => void;
}

function DraggablePinnedSection(props: DraggablePinnedSectionProps): JSX.Element {
  const { slugs, selectedLower, busy, onSelect, onTogglePin, onReorderPin } = props;
  const { itemState, dragHandlers } = usePinDragReorder(busy, onReorderPin);
  // After a keyboard move the list re-renders in a new order; keep focus on the
  // row the user is moving so repeated ArrowUp/ArrowDown presses keep working.
  const handleRefs = useRef<Map<string, HTMLButtonElement | null>>(new Map());
  const pendingFocusSlug = useRef<string | null>(null);

  // Keep keyboard focus on the row the user is moving. After an arrow-key
  // reorder the list re-renders in the new order and the grip is briefly
  // disabled while the durable write is in flight; depend on `busy` and only
  // clear the pending focus once the (now-enabled) handle actually takes focus,
  // so a keyboard user can chain ArrowUp/ArrowDown moves without losing place.
  useEffect(() => {
    const slug = pendingFocusSlug.current;
    if (!slug) return;
    const handle = handleRefs.current.get(slug.toLowerCase());
    if (handle && !handle.disabled) {
      pendingFocusSlug.current = null;
      handle.focus();
    }
  }, [slugs, busy]);

  const moveByKeyboard = useCallback(
    (slug: string, direction: -1 | 1) => {
      if (busy) return;
      const index = slugs.findIndex((s) => s.toLowerCase() === slug.toLowerCase());
      const targetIndex = index + direction;
      if (index === -1 || targetIndex < 0 || targetIndex >= slugs.length) return;
      pendingFocusSlug.current = slug;
      onReorderPin(slug, slugs[targetIndex]!);
    },
    [slugs, busy, onReorderPin],
  );

  return (
    <div className="home-repos-recent">
      <div className="home-repos-recent-label">
        Pinned
        {slugs.length > 1 && (
          <span className="home-repos-recent-count"> (drag or use arrow keys to reorder)</span>
        )}
      </div>
      <ul className="home-repos-recent-list home-repos-pinned-reorder" role="list">
        {slugs.map((slug, index) => {
          const alreadyPicked = selectedLower.has(slug.toLowerCase());
          const { isDragging, isDropTarget } = itemState(slug);
          const itemClass =
            "home-repos-recent-item home-repos-pinned-reorder-item" +
            (isDragging ? " is-dragging" : "") +
            (isDropTarget ? " is-drop-target" : "");
          return (
            <li
              key={`Pinned:${slug}`}
              className={itemClass}
              aria-label={`Pinned repository ${slug}, position ${index + 1} of ${slugs.length}`}
              {...dragHandlers(slug)}
            >
              <button
                type="button"
                className="home-repos-drag-handle"
                ref={(el) => handleRefs.current.set(slug.toLowerCase(), el)}
                disabled={busy}
                aria-label={`Reorder ${slug}. Use arrow up and arrow down keys, or drag.`}
                aria-keyshortcuts="ArrowUp ArrowDown"
                title="Drag to reorder, or focus and use arrow keys"
                onKeyDown={(e) => {
                  if (e.key === "ArrowUp") {
                    e.preventDefault();
                    moveByKeyboard(slug, -1);
                  } else if (e.key === "ArrowDown") {
                    e.preventDefault();
                    moveByKeyboard(slug, 1);
                  }
                }}
              >
                <GripVertical aria-hidden="true" className="home-repos-drag-handle-icon" />
              </button>
              <button
                type="button"
                className={"home-repos-recent-chip" + (alreadyPicked ? " is-selected" : "")}
                onClick={(e) => onSelect(slug, repoSelectModeFromEvent(e))}
                disabled={busy}
                aria-pressed={alreadyPicked}
                title={
                  alreadyPicked
                    ? `${slug} (selected) — click to keep only this, Shift-click to add`
                    : `${slug} — click to select, Shift-click to add`
                }
              >
                {slug}
              </button>
              <AdditiveAddButton
                slug={slug}
                selected={alreadyPicked}
                busy={busy}
                onSelect={onSelect}
              />
              <PinToggleButton
                slug={slug}
                pinned
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

// AdditiveAddButton is the explicit "+" affordance the requirement calls for:
// a non-keyboard, non-Shift way to ADD a repo to the current selection. Plain
// clicking the repo name is exclusive (it becomes the only staged repo), so
// this button is how a pointer user keeps the existing selection and appends
// one more. It is disabled when the repo is already staged, because additive-
// adding a repo already in the set is a no-op — the disabled state communicates
// "already selected" instead of presenting a button that does nothing.
interface AdditiveAddButtonProps {
  slug: string;
  selected: boolean;
  busy: boolean;
  onSelect: RepoSelectHandler;
}

function AdditiveAddButton(props: AdditiveAddButtonProps): JSX.Element {
  const { slug, selected, busy, onSelect } = props;
  return (
    <button
      type="button"
      className="home-repos-add-more"
      onClick={() => onSelect(slug, "additive")}
      disabled={busy || selected}
      aria-label={
        selected ? `${slug} already selected` : `Add ${slug} to selection`
      }
      title={
        selected ? `${slug} already selected` : `Add ${slug} (keep current selection)`
      }
    >
      <PlusIcon aria-hidden="true" className="home-repos-add-more-icon" />
    </button>
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
