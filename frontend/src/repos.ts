// Repo-selection helpers shared between the splash picker
// (RepoPicker), the App.tsx integration, and unit tests.
//
// Mirrors the backend's handlers_repos.go boundary rules so the
// happy-path UX is "the server accepts what the UI accepts":
//
//   - REPO_SLUG_PATTERN matches the same regex as repoSlugPattern.
//   - MAX_REPOS_PER_SESSION matches maxReposPerSession.
//   - REPO_SUPPORTED_MODES matches sessionModeSupportsRepos.
//
// Changing one without the other is a UX regression (server rejects
// what UI accepted) or a defense-in-depth regression (UI accepts
// what server rightfully refuses), so the values are documented as
// load-bearing on both sides.

import { REPO_SUPPORTED_MODES, type SessionMode } from "./sessionModes";

export { REPO_SUPPORTED_MODES } from "./sessionModes";

export const MAX_REPOS_PER_SESSION = 5;
export const MAX_PINNED_REPOS = 64;
export const RECENT_REPO_PREVIEW_LIMIT = 4;

export const REPO_SLUG_PATTERN =
  /^[A-Za-z0-9][A-Za-z0-9-]{0,38}\/[A-Za-z0-9._-]{1,100}$/;

export function isValidRepoSlug(value: string): boolean {
  return REPO_SLUG_PATTERN.test(value.trim());
}

export function modeSupportsRepos(mode: string): boolean {
  return REPO_SUPPORTED_MODES.has(mode as SessionMode);
}

export function normalizeRepoSlugs(
  raw: readonly string[],
  limit = MAX_REPOS_PER_SESSION,
): string[] {
  if (limit <= 0) return [];

  const seen = new Set<string>();
  const out: string[] = [];
  for (const rawSlug of raw) {
    const slug = rawSlug.trim();
    if (!isValidRepoSlug(slug)) continue;
    const key = slug.toLowerCase();
    if (seen.has(key)) continue;
    seen.add(key);
    out.push(slug);
    if (out.length >= limit) break;
  }
  return out;
}

export function recentRepoPreviewSlugs(
  recent: string[],
  selected: string[],
  limit = RECENT_REPO_PREVIEW_LIMIT,
): string[] {
  if (limit <= 0 || selected.length >= MAX_REPOS_PER_SESSION) return [];

  const selectedLower = new Set(
    selected.map((slug) => slug.trim().toLowerCase()),
  );
  const seen = new Set<string>();
  const out: string[] = [];
  for (const rawSlug of recent) {
    const slug = rawSlug.trim();
    if (!isValidRepoSlug(slug)) continue;
    const key = slug.toLowerCase();
    if (selectedLower.has(key) || seen.has(key)) continue;
    seen.add(key);
    out.push(slug);
    if (out.length >= limit) break;
  }
  return out;
}

export function recentRepoShortcutSlugs(
  recent: string[],
  limit = RECENT_REPO_PREVIEW_LIMIT,
): string[] {
  if (limit <= 0) return [];

  const seen = new Set<string>();
  const out: string[] = [];
  for (const rawSlug of recent) {
    const slug = rawSlug.trim();
    if (!isValidRepoSlug(slug)) continue;
    const key = slug.toLowerCase();
    if (seen.has(key)) continue;
    seen.add(key);
    out.push(slug);
    if (out.length >= limit) break;
  }
  return out;
}

export function pinnedRepoSlugs(pinned: readonly string[]): string[] {
  return normalizeRepoSlugs(pinned, MAX_PINNED_REPOS);
}

export function repoShortcutSlugs(
  pinned: readonly string[],
  recent: readonly string[],
  limit = RECENT_REPO_PREVIEW_LIMIT,
): string[] {
  if (limit <= 0) return [];

  const seen = new Set<string>();
  const out: string[] = [];
  for (const rawSlug of [...pinned, ...recent]) {
    const slug = rawSlug.trim();
    if (!isValidRepoSlug(slug)) continue;
    const key = slug.toLowerCase();
    if (seen.has(key)) continue;
    seen.add(key);
    out.push(slug);
    if (out.length >= limit) break;
  }
  return out;
}

export function isRepoPinned(pinned: readonly string[], slug: string): boolean {
  const key = slug.trim().toLowerCase();
  return key !== "" && pinned.some((existing) => existing.toLowerCase() === key);
}

export function pinRepoSlug(pinned: readonly string[], rawSlug: string): string[] {
  const slug = rawSlug.trim();
  if (!isValidRepoSlug(slug) || isRepoPinned(pinned, slug)) {
    return pinnedRepoSlugs(pinned);
  }
  return [...pinnedRepoSlugs(pinned), slug];
}

export function unpinRepoSlug(pinned: readonly string[], slug: string): string[] {
  const key = slug.trim().toLowerCase();
  return pinnedRepoSlugs(pinned).filter(
    (existing) => existing.toLowerCase() !== key,
  );
}

// reorderPinnedRepoSlugs moves `sourceSlug` to sit relative to `targetSlug`
// within the durable pin list, returning the normalized reordered list. This
// is the pure core of the splash picker's drag-and-drop (and keyboard)
// reordering: the durable `profiles.pinned_repos text[]` order IS the pin
// order, so a reorder is just a PUT of the reordered list — there is no
// separate "order" field and no backend logic change beyond preserving array
// order through validation (which it already does).
//
// The insert is direction-aware so both list ends stay reachable from a single
// drop target: dragging a slug downward (toward a later index) lands it AFTER
// the target; dragging upward lands it BEFORE the target. Without this, an
// "always insert before target" rule could never move an item to the final
// position by dropping on the last item.
//
// Slugs are matched case-insensitively (mirroring isRepoPinned) so the caller
// can pass whatever casing the rendered chip used. Unknown slugs, equal
// source/target, or an effective no-op all return the normalized list
// unchanged, so a stray drag can never corrupt the list.
export function reorderPinnedRepoSlugs(
  pinned: readonly string[],
  sourceSlug: string,
  targetSlug: string,
): string[] {
  const normalized = pinnedRepoSlugs(pinned);
  const source = sourceSlug.trim().toLowerCase();
  const target = targetSlug.trim().toLowerCase();
  if (source === "" || target === "" || source === target) {
    return normalized;
  }
  const fromIndex = normalized.findIndex((s) => s.toLowerCase() === source);
  const toIndex = normalized.findIndex((s) => s.toLowerCase() === target);
  if (fromIndex === -1 || toIndex === -1) {
    return normalized;
  }
  const next = [...normalized];
  const [moved] = next.splice(fromIndex, 1);
  const targetAfterRemoval = next.findIndex((s) => s.toLowerCase() === target);
  const insertIndex = targetAfterRemoval + (fromIndex < toIndex ? 1 : 0);
  next.splice(insertIndex, 0, moved);
  return next;
}

// addRepoSlug encapsulates the picker's add-to-staged logic in a pure
// function. Returns either {ok: true, next: string[]} on a successful
// add or {ok: false, error: string} with the user-facing reason.
// Centralizing the rules here keeps the splash-page integration in
// App.tsx mechanical and gives the test suite one place to enforce
// the contract.
export type AddRepoResult =
  | { ok: true; next: string[] }
  | { ok: false; error: string };

export function addRepoSlug(current: string[], rawSlug: string): AddRepoResult {
  const slug = rawSlug.trim();
  if (slug === "") {
    return { ok: false, error: "Repository slug is empty" };
  }
  if (!isValidRepoSlug(slug)) {
    return { ok: false, error: `"${slug}" doesn't look like owner/name` };
  }
  if (
    current.some(
      (existing) => existing.toLowerCase() === slug.toLowerCase(),
    )
  ) {
    return { ok: false, error: `${slug} is already added` };
  }
  if (current.length >= MAX_REPOS_PER_SESSION) {
    return {
      ok: false,
      error: `At most ${MAX_REPOS_PER_SESSION} repos per session`,
    };
  }
  return { ok: true, next: [...current, slug] };
}

// RepoSelectionMode is the gesture intent behind a splash-picker repo pick.
//
//   - "exclusive": a bare number shortcut or a plain click. The staged set
//     becomes exactly the chosen slug, mirroring a single-select list.
//   - "additive": shift+number, shift-click, or the explicit "+" affordance.
//     The slug is unioned into the current set, keeping prior picks.
//
// Both gestures flow through applyRepoSelection so the exclusive/additive split
// lives in one tested place instead of being re-derived at every call site.
export type RepoSelectionMode = "exclusive" | "additive";

// applyRepoSelection is the splash picker's selection core for chip/shortcut
// gestures. Exclusive mode replaces the staged set with the single chosen slug;
// additive mode unions the slug into the current set. Both share the same slug
// validation as addRepoSlug, so the two gestures reject identical malformed
// input.
//
// Additive is intentionally idempotent: re-adding an already-staged repo is a
// no-op success rather than the "already added" error addRepoSlug returns. A
// multi-select gesture (shift-click, "+") on a repo that is already in the set
// should simply leave it in the set, not raise an error the user did not cause.
// The explicit typed-entry Add button keeps using addRepoSlug precisely because
// a duplicate there is worth surfacing. The 5-repo cap still bounds additive;
// exclusive can never exceed it because its result is a single slug.
export function applyRepoSelection(
  current: string[],
  rawSlug: string,
  mode: RepoSelectionMode,
): AddRepoResult {
  const slug = rawSlug.trim();
  if (slug === "") {
    return { ok: false, error: "Repository slug is empty" };
  }
  if (!isValidRepoSlug(slug)) {
    return { ok: false, error: `"${slug}" doesn't look like owner/name` };
  }
  if (mode === "exclusive") {
    return { ok: true, next: [slug] };
  }
  const alreadyStaged = current.some(
    (existing) => existing.toLowerCase() === slug.toLowerCase(),
  );
  if (alreadyStaged) {
    return { ok: true, next: current };
  }
  if (current.length >= MAX_REPOS_PER_SESSION) {
    return {
      ok: false,
      error: `At most ${MAX_REPOS_PER_SESSION} repos per session`,
    };
  }
  return { ok: true, next: [...current, slug] };
}

export function removeRepoSlug(current: string[], slug: string): string[] {
  return current.filter((existing) => existing !== slug);
}
