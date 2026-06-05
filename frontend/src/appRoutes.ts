export type SettingsTab = "preferences" | "admin";
export type AdminView = "controls" | "avatars" | "report" | "observability";
export type SessionRouteTab = "chat" | "turns" | "static";
export type HomeRouteTab = "chat";
export type AppRouteTab = "settings" | "help";

export type SettingsRoute = {
  settingsTab: SettingsTab;
  adminView: AdminView;
};

export type SessionRoute = SettingsRoute & {
  sessionId: string;
  tab: SessionRouteTab;
  // The durable per-session turn number (session_turns.turn_number). null when
  // there is no turn segment, or when the segment is not a positive integer
  // (e.g. a bookmarked legacy turn_<uuid>). turnSegmentPresent distinguishes
  // "no turn requested" from "a turn was requested but isn't a valid number" so
  // the SPA can show an explicit unavailable-target state instead of silently
  // defaulting to the latest turn.
  turnNumber: number | null;
  turnSegmentPresent: boolean;
  // Workspace-relative path of the HTML file to render full-page in the
  // sandboxed "static" view. Non-null only when tab === "static".
  staticPath: string | null;
};

export type HomeRoute = SettingsRoute & {
  tab: HomeRouteTab;
};

export type AppRoute = SettingsRoute & {
  tab: AppRouteTab;
};

const defaultSettingsRoute: SettingsRoute = {
  settingsTab: "preferences",
  adminView: "controls",
};

export function decodeRouteSegment(segment: string): string {
  try {
    return decodeURIComponent(segment);
  } catch {
    return "";
  }
}

function routeParts(pathname: string): string[] {
  return pathname.split("/").filter(Boolean).map(decodeRouteSegment);
}

function readSettingsRoute(parts: string[]): SettingsRoute {
  if (parts[0] === "admin") {
    const adminView = parseAdminView(parts[1]);
    return {
      settingsTab: "admin",
      adminView,
    };
  }
  return defaultSettingsRoute;
}

function parseAdminView(value: string | undefined): AdminView {
  switch (value) {
    case undefined:
    case "":
    case "controls":
      return "controls";
    case "avatars":
    case "report":
    case "observability":
      return value;
    default:
      return "controls";
  }
}

// parseTurnNumber accepts only a bare positive integer (no sign, no leading
// zero, no decimal). Anything else — including a legacy turn_<uuid> someone has
// bookmarked — yields null so the caller can route it to the unavailable-target
// state rather than guessing.
export function parseTurnNumber(segment: string): number | null {
  if (!/^[1-9][0-9]*$/.test(segment)) return null;
  const value = Number(segment);
  return Number.isSafeInteger(value) ? value : null;
}

export function readSessionRouteFromPathname(pathname: string): SessionRoute | null {
  const parts = routeParts(pathname);
  if (parts[0] !== "sessions" || !parts[1]) return null;
  if (parts.length === 2) {
    return {
      sessionId: parts[1],
      tab: "chat",
      turnNumber: null,
      turnSegmentPresent: false,
      staticPath: null,
      ...defaultSettingsRoute,
    };
  }
  if (parts[2] === "turns") {
    const segment = parts[3]?.trim() ?? "";
    return {
      sessionId: parts[1],
      tab: "turns",
      turnNumber: parseTurnNumber(segment),
      turnSegmentPresent: segment !== "",
      staticPath: null,
      ...defaultSettingsRoute,
    };
  }
  if (parts[2] === "static" && parts.length >= 4) {
    // Everything after /static/ is the workspace-relative file path. Reject any
    // `..` segment so a bookmarked link can't escape the workspace (the backend
    // re-validates with safeWorkspacePath too).
    const rel = parts.slice(3).join("/");
    if (!rel || rel.split("/").some((seg) => seg === "..")) return null;
    return {
      sessionId: parts[1],
      tab: "static",
      turnNumber: null,
      turnSegmentPresent: false,
      staticPath: rel,
      ...defaultSettingsRoute,
    };
  }
  return null;
}

export function readHomeRouteFromPathname(pathname: string): HomeRoute | null {
  const parts = routeParts(pathname);
  if (parts[0] !== "new") return null;
  if (parts.length === 1) return { tab: "chat", ...defaultSettingsRoute };
  return null;
}

export function readAppRouteFromPathname(pathname: string): AppRoute | null {
  const parts = routeParts(pathname);
  if (parts[0] === "settings") {
    return {
      tab: "settings",
      ...readSettingsRoute(parts.slice(1)),
    };
  }
  if (parts[0] === "help") return { tab: "help", ...defaultSettingsRoute };
  return null;
}

function settingsPath(settingsTab: SettingsTab, adminView: AdminView): string {
  if (settingsTab !== "admin") return "/settings";
  return `/settings/admin${adminView === "controls" ? "" : `/${adminView}`}`;
}

export function buildSessionRouteUrl(
  currentHref: string,
  id: string,
  tab: SessionRouteTab = "chat",
  turnNumber?: number | null,
  staticPath?: string | null,
): string {
  const url = new URL(currentHref);
  const encodedId = encodeURIComponent(id);
  let suffix = "";
  if (tab === "turns") {
    suffix = `/turns${turnNumber != null ? `/${turnNumber}` : ""}`;
  } else if (tab === "static" && staticPath) {
    suffix = `/static/${staticPath.split("/").map(encodeURIComponent).join("/")}`;
  }
  url.pathname = `/sessions/${encodedId}${suffix}`;
  url.search = "";
  url.hash = "";
  return url.toString();
}

export function buildHomeRouteUrl(
  currentHref: string,
  _tab: HomeRouteTab = "chat",
): string {
  const url = new URL(currentHref);
  url.pathname = "/new";
  url.search = "";
  url.hash = "";
  return url.toString();
}

export function buildAppRouteUrl(
  currentHref: string,
  tab: AppRouteTab,
  settingsTab: SettingsTab = "preferences",
  adminView: AdminView = "controls",
): string {
  const url = new URL(currentHref);
  url.pathname = `${
    tab === "settings"
      ? settingsPath(settingsTab, adminView)
      : tab === "help"
        ? "/help"
        : "/settings"
  }`;
  url.search = "";
  url.hash = "";
  return url.toString();
}
