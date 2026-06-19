export type SettingsTab = "preferences" | "admin";
export type AdminView =
  | "controls"
  | "avatars"
  | "report"
  | "break-glass"
  | "orchestrations"
  | "hidden-transcripts"
  | "observability"
  | "version";
export type SessionRouteTab =
  | "turns"
  | "chat"
  | "static"
  | "session-data"
  | "queue-status"
  | "pull-requests"
  | "break-glass"
  | "test-slot-model"
  | "files"
  | "background"
  | "orchestrate";
export type HomeRouteTab = "chat";
export type AppRouteTab = "settings" | "help" | "cluster";

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
  // The 1-based activity page ordinal within the selected turn (the
  // turnActivityPager page). null when there is no /pages segment, or the
  // segment isn't a positive integer. pageSegmentPresent distinguishes "no page
  // requested" (canonicalize to the server default) from "a page was requested
  // but isn't valid" (explicit unavailable-target) — the same split as turns.
  pageNumber: number | null;
  pageSegmentPresent: boolean;
  // Workspace-relative path of the HTML file to render full-page in the
  // sandboxed "static" view. Non-null only when tab === "static".
  staticPath: string | null;
  // Workspace-relative file selected in the files view. Non-null only when
  // tab === "files" and the route includes a file target.
  filePath: string | null;
  fileLine: number | null;
  // Control-action event id selected in the break-glass approval view.
  // Non-null only when tab === "break-glass".
  breakGlassRequestId: string | null;
  // Control-action event id selected in the test-slot model approval view.
  // Non-null only when tab === "test-slot-model".
  testSlotModelRequestId: string | null;
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
    case "break-glass":
    case "orchestrations":
    case "report":
    case "hidden-transcripts":
    case "observability":
    case "version":
      return value;
    default:
      return "controls";
  }
}

// parsePositiveIntSegment accepts only a bare positive integer (no sign, no
// leading zero, no decimal). Anything else yields null so the caller can route
// it to an explicit unavailable-target state rather than guessing. It is the
// single source for the turn- and page-number parsing discipline.
function parsePositiveIntSegment(segment: string): number | null {
  if (!/^[1-9][0-9]*$/.test(segment)) return null;
  const value = Number(segment);
  return Number.isSafeInteger(value) ? value : null;
}

// parseTurnNumber resolves the durable per-session turn number from a route
// segment. A bookmarked legacy turn_<uuid> yields null → unavailable-target.
export function parseTurnNumber(segment: string): number | null {
  return parsePositiveIntSegment(segment);
}

// parsePageNumber resolves the 1-based activity page ordinal from a route
// segment, with the same discipline as turn numbers (bad → unavailable-target).
export function parsePageNumber(segment: string): number | null {
  return parsePositiveIntSegment(segment);
}

function splitFileLineSuffix(path: string): {
  path: string;
  line: number | null;
} {
  const match = path.match(/:(\d+)$/);
  if (!match) return { path, line: null };
  const line = Number(match[1]);
  if (!Number.isSafeInteger(line) || line < 1) return { path, line: null };
  return { path: path.slice(0, -match[0].length), line };
}

function validWorkspaceRoutePath(path: string): boolean {
  return Boolean(path) && !path.split("/").some((seg) => seg === "..");
}

export function readSessionRouteFromPathname(pathname: string): SessionRoute | null {
  const parts = routeParts(pathname);
  if (parts[0] !== "sessions" || !parts[1]) return null;
  if (parts.length === 2) {
    return {
      sessionId: parts[1],
      tab: "turns",
      turnNumber: null,
      turnSegmentPresent: false,
      pageNumber: null,
      pageSegmentPresent: false,
      staticPath: null,
      filePath: null,
      fileLine: null,
      breakGlassRequestId: null,
      testSlotModelRequestId: null,
      ...defaultSettingsRoute,
    };
  }
  if (parts[2] === "transcript" && parts.length === 3) {
    return {
      sessionId: parts[1],
      tab: "chat",
      turnNumber: null,
      turnSegmentPresent: false,
      pageNumber: null,
      pageSegmentPresent: false,
      staticPath: null,
      filePath: null,
      fileLine: null,
      breakGlassRequestId: null,
      testSlotModelRequestId: null,
      ...defaultSettingsRoute,
    };
  }
  if (parts[2] === "turns") {
    const turnSegment = parts[3]?.trim() ?? "";
    // A turns route may carry an explicit /pages/{p} ordinal. Anything other
    // than "pages" in the fourth slot is an unknown subroute and is rejected
    // rather than silently ignored, so a malformed link surfaces as a miss.
    let pageNumber: number | null = null;
    let pageSegmentPresent = false;
    if (parts.length >= 5) {
      if (parts[4] !== "pages") return null;
      const pageSegment = parts[5]?.trim() ?? "";
      pageNumber = parsePageNumber(pageSegment);
      pageSegmentPresent = pageSegment !== "";
    }
    return {
      sessionId: parts[1],
      tab: "turns",
      turnNumber: parseTurnNumber(turnSegment),
      turnSegmentPresent: turnSegment !== "",
      pageNumber,
      pageSegmentPresent,
      staticPath: null,
      filePath: null,
      fileLine: null,
      breakGlassRequestId: null,
      testSlotModelRequestId: null,
      ...defaultSettingsRoute,
    };
  }
  if (parts[2] === "static" && parts.length >= 4) {
    // Everything after /static/ is the workspace-relative file path. Reject any
    // `..` segment so a bookmarked link can't escape the workspace (the backend
    // re-validates with safeWorkspacePath too).
    const rel = parts.slice(3).join("/");
    if (!validWorkspaceRoutePath(rel)) return null;
    return {
      sessionId: parts[1],
      tab: "static",
      turnNumber: null,
      turnSegmentPresent: false,
      pageNumber: null,
      pageSegmentPresent: false,
      staticPath: rel,
      filePath: null,
      fileLine: null,
      breakGlassRequestId: null,
      testSlotModelRequestId: null,
      ...defaultSettingsRoute,
    };
  }
  if (parts[2] === "session-data" && parts.length === 3) {
    return {
      sessionId: parts[1],
      tab: "session-data",
      turnNumber: null,
      turnSegmentPresent: false,
      pageNumber: null,
      pageSegmentPresent: false,
      staticPath: null,
      filePath: null,
      fileLine: null,
      breakGlassRequestId: null,
      testSlotModelRequestId: null,
      ...defaultSettingsRoute,
    };
  }
  if (parts[2] === "queue-status" && parts.length === 3) {
    return {
      sessionId: parts[1],
      tab: "queue-status",
      turnNumber: null,
      turnSegmentPresent: false,
      pageNumber: null,
      pageSegmentPresent: false,
      staticPath: null,
      filePath: null,
      fileLine: null,
      breakGlassRequestId: null,
      testSlotModelRequestId: null,
      ...defaultSettingsRoute,
    };
  }
  // The file-browser pane is routed at the surface level, and may include a
  // workspace-relative file path so transcript file links can open the same
  // preview in a new tab.
  if (parts[2] === "pull-requests" && parts.length === 3) {
    return {
      sessionId: parts[1],
      tab: "pull-requests",
      turnNumber: null,
      turnSegmentPresent: false,
      pageNumber: null,
      pageSegmentPresent: false,
      staticPath: null,
      filePath: null,
      fileLine: null,
      breakGlassRequestId: null,
      testSlotModelRequestId: null,
      ...defaultSettingsRoute,
    };
  }
  if (parts[2] === "break-glass" && parts.length === 4 && parts[3].trim()) {
    return {
      sessionId: parts[1],
      tab: "break-glass",
      turnNumber: null,
      turnSegmentPresent: false,
      pageNumber: null,
      pageSegmentPresent: false,
      staticPath: null,
      filePath: null,
      fileLine: null,
      breakGlassRequestId: parts[3],
      testSlotModelRequestId: null,
      ...defaultSettingsRoute,
    };
  }
  if (parts[2] === "test-slot-model" && parts.length === 4 && parts[3].trim()) {
    return {
      sessionId: parts[1],
      tab: "test-slot-model",
      turnNumber: null,
      turnSegmentPresent: false,
      pageNumber: null,
      pageSegmentPresent: false,
      staticPath: null,
      filePath: null,
      fileLine: null,
      breakGlassRequestId: null,
      testSlotModelRequestId: parts[3],
      ...defaultSettingsRoute,
    };
  }
  if (parts[2] === "files" && parts.length >= 3) {
    const target = parts.length > 3 ? parts.slice(3).join("/") : "";
    if (target && !validWorkspaceRoutePath(target)) return null;
    const fileTarget = target ? splitFileLineSuffix(target) : null;
    return {
      sessionId: parts[1],
      tab: "files",
      turnNumber: null,
      turnSegmentPresent: false,
      pageNumber: null,
      pageSegmentPresent: false,
      staticPath: null,
      filePath: fileTarget?.path ?? null,
      fileLine: fileTarget?.line ?? null,
      breakGlassRequestId: null,
      testSlotModelRequestId: null,
      ...defaultSettingsRoute,
    };
  }
  if (parts[2] === "background" && parts.length === 3) {
    return {
      sessionId: parts[1],
      tab: "background",
      turnNumber: null,
      turnSegmentPresent: false,
      pageNumber: null,
      pageSegmentPresent: false,
      staticPath: null,
      filePath: null,
      fileLine: null,
      breakGlassRequestId: null,
      testSlotModelRequestId: null,
      ...defaultSettingsRoute,
    };
  }
  if (parts[2] === "orchestrate" && parts.length === 3) {
    return {
      sessionId: parts[1],
      tab: "orchestrate",
      turnNumber: null,
      turnSegmentPresent: false,
      pageNumber: null,
      pageSegmentPresent: false,
      staticPath: null,
      filePath: null,
      fileLine: null,
      breakGlassRequestId: null,
      testSlotModelRequestId: null,
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
  if (parts[0] === "cluster") return { tab: "cluster", ...defaultSettingsRoute };
  return null;
}

function settingsPath(settingsTab: SettingsTab, adminView: AdminView): string {
  if (settingsTab !== "admin") return "/settings";
  return `/settings/admin${adminView === "controls" ? "" : `/${adminView}`}`;
}

export function buildSessionRouteUrl(
  currentHref: string,
  id: string,
  tab: SessionRouteTab = "turns",
  turnNumber?: number | null,
  staticPath?: string | null,
  pageNumber?: number | null,
  filePath?: string | null,
  fileLine?: number | null,
  breakGlassRequestId?: string | null,
  testSlotModelRequestId?: string | null,
): string {
  const url = new URL(currentHref);
  const encodedId = encodeURIComponent(id);
  let suffix = "";
  if (tab === "turns") {
    suffix = turnNumber != null ? `/turns/${turnNumber}` : "";
    // A page ordinal qualifies a specific numbered turn; the bare turns route
    // (/sessions/{id} = latest) canonicalizes its own page once resolved.
    if (turnNumber != null && pageNumber != null) {
      suffix += `/pages/${pageNumber}`;
    }
  } else if (tab === "chat") {
    suffix = "/transcript";
  } else if (tab === "static" && staticPath) {
    suffix = `/static/${staticPath.split("/").map(encodeURIComponent).join("/")}`;
  } else if (tab === "session-data") {
    suffix = "/session-data";
  } else if (tab === "queue-status") {
    suffix = "/queue-status";
  } else if (tab === "pull-requests") {
    suffix = "/pull-requests";
  } else if (tab === "break-glass" && breakGlassRequestId) {
    suffix = `/break-glass/${encodeURIComponent(breakGlassRequestId)}`;
  } else if (tab === "test-slot-model" && testSlotModelRequestId) {
    suffix = `/test-slot-model/${encodeURIComponent(testSlotModelRequestId)}`;
  } else if (tab === "files") {
    const routedFilePath = filePath
      ? filePath.split("/").map(encodeURIComponent).join("/")
      : "";
    suffix = `/files${
      routedFilePath
        ? `/${routedFilePath}${fileLine != null ? `:${fileLine}` : ""}`
        : ""
    }`;
  } else if (tab === "background") {
    suffix = "/background";
  } else if (tab === "orchestrate") {
    suffix = "/orchestrate";
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
        : tab === "cluster"
          ? "/cluster"
          : "/settings"
  }`;
  url.search = "";
  url.hash = "";
  return url.toString();
}
