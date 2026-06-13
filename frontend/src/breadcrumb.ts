import { buildSessionRouteUrl, type SessionRouteTab } from "./appRoutes";

// One breadcrumb segment AFTER the session-name crumb. The name crumb (which
// links to the session-data root) is owned by the title chrome; this module owns
// the section/turn/page trail so the derivation can be unit-tested without a DOM.
export type BreadcrumbCrumb = {
  key: string;
  label: string;
  // Navigation target, or null for a structural, non-navigable label
  // ("pages", "files") — see also `current`.
  href: string | null;
  // The current location's leaf: rendered as a non-interactive marker even when
  // it has a conceptual href, so a crumb never links to where you already are.
  current: boolean;
};

// The in-session location the trail reflects, bubbled up from the visible pane.
export type BreadcrumbLocation = {
  tab: string;
  turnNumber: number | null;
  pageNumber: number | null;
  staticPath: string | null;
  turnUnavailable: boolean;
};

// Pure derivation of the breadcrumb trail for a given location. Climb-only:
// ancestors are links, the leaf is `current`. Returns [] for the session-data
// root (the name crumb is its own leaf) and for app-level / not-yet-routed tabs.
export function breadcrumbTrail(
  sessionId: string,
  location: BreadcrumbLocation,
  currentHref: string,
): BreadcrumbCrumb[] {
  const url = (
    tab: SessionRouteTab,
    turnNumber: number | null,
    pageNumber: number | null,
    staticPath: string | null,
  ) =>
    buildSessionRouteUrl(
      currentHref,
      sessionId,
      tab,
      turnNumber,
      staticPath,
      pageNumber,
    );

  if (location.tab === "chat") {
    return [
      {
        key: "section",
        label: "main transcript",
        href: url("chat", null, null, null),
        current: true,
      },
    ];
  }

  if (location.tab === "turns") {
    const section: BreadcrumbCrumb = {
      key: "section",
      label: "turns",
      href: url("turns", null, null, null),
      current: false,
    };
    if (location.turnUnavailable) {
      return [
        section,
        { key: "turn", label: "unavailable", href: null, current: true },
      ];
    }
    const hasPage = location.pageNumber != null;
    const crumbs: BreadcrumbCrumb[] = [
      section,
      {
        key: "turn",
        label: location.turnNumber != null ? String(location.turnNumber) : "current",
        href: url("turns", location.turnNumber, null, null),
        current: !hasPage,
      },
    ];
    if (hasPage) {
      crumbs.push({ key: "pages", label: "pages", href: null, current: false });
      crumbs.push({
        key: "page",
        label: String(location.pageNumber),
        href: url("turns", location.turnNumber, location.pageNumber, null),
        current: true,
      });
    }
    return crumbs;
  }

  if (location.tab === "static" && location.staticPath) {
    return [
      { key: "files", label: "files", href: null, current: false },
      {
        key: "path",
        label: location.staticPath,
        href: url("static", null, null, location.staticPath),
        current: true,
      },
    ];
  }

  if (
    location.tab === "files" ||
    location.tab === "background" ||
    location.tab === "pull-requests"
  ) {
    return [
      {
        key: "section",
        label: location.tab,
        href: url(location.tab, null, null, null),
        current: true,
      },
    ];
  }

  return [];
}

// A compact one-line label of the current location for the mobile top bar,
// which can't fit the full trail. null at the base (chat) and the session-data
// root, where the bar shows the session name instead.
export function breadcrumbCompactLabel(
  location: BreadcrumbLocation,
): string | null {
  if (location.tab === "chat" || location.tab === "session-data") return null;
  const labels = breadcrumbTrail("", location, "https://x/").map((c) => c.label);
  return labels.length > 0 ? labels.join(" / ") : null;
}

// The parent location to climb to from where you are now (the mobile back/up
// affordance), or null at the root. Walks the navigable ancestors — the
// session-data root, then the trail's linkable crumbs — and returns the nearest
// one above the current leaf with a distinct target.
export function breadcrumbUpHref(
  sessionId: string,
  location: BreadcrumbLocation,
  currentHref: string,
): string | null {
  const rootHref = buildSessionRouteUrl(currentHref, sessionId, "session-data");
  const navigable = [
    rootHref,
    ...breadcrumbTrail(sessionId, location, currentHref)
      .filter((c) => c.href != null)
      .map((c) => c.href as string),
  ];
  if (navigable.length <= 1) return null;
  const leaf = navigable[navigable.length - 1];
  for (let i = navigable.length - 2; i >= 0; i--) {
    if (navigable[i] !== leaf) return navigable[i];
  }
  return null;
}
