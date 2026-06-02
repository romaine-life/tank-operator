export type SettingsTab = "preferences" | "admin";
export type AdminView = "controls" | "avatars" | "report" | "observability";
export type SessionRouteTab = "chat" | "turns" | "settings" | "help";
export type HomeRouteTab = "chat" | "settings" | "help";

export type SettingsRoute = {
  settingsTab: SettingsTab;
  adminView: AdminView;
};

export type SessionRoute = SettingsRoute & {
  sessionId: string;
  tab: SessionRouteTab;
  turnId: string | null;
};

export type HomeRoute = SettingsRoute & {
  tab: HomeRouteTab;
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

export function readSessionRouteFromPathname(pathname: string): SessionRoute | null {
  const parts = routeParts(pathname);
  if (parts[0] !== "sessions" || !parts[1]) return null;
  if (parts.length === 2) {
    return { sessionId: parts[1], tab: "chat", turnId: null, ...defaultSettingsRoute };
  }
  if (parts[2] === "turns") {
    return {
      sessionId: parts[1],
      tab: "turns",
      turnId: parts[3]?.trim() || null,
      ...defaultSettingsRoute,
    };
  }
  if (parts[2] === "settings") {
    return {
      sessionId: parts[1],
      tab: "settings",
      turnId: null,
      ...readSettingsRoute(parts.slice(3)),
    };
  }
  if (parts[2] === "help") {
    return { sessionId: parts[1], tab: "help", turnId: null, ...defaultSettingsRoute };
  }
  return { sessionId: parts[1], tab: "chat", turnId: null, ...defaultSettingsRoute };
}

export function readHomeRouteFromPathname(pathname: string): HomeRoute | null {
  const parts = routeParts(pathname);
  if (parts[0] !== "new") return null;
  if (parts.length === 1) return { tab: "chat", ...defaultSettingsRoute };
  if (parts[1] === "settings") {
    return {
      tab: "settings",
      ...readSettingsRoute(parts.slice(2)),
    };
  }
  if (parts[1] === "help") return { tab: "help", ...defaultSettingsRoute };
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
  turnId?: string | null,
  settingsTab: SettingsTab = "preferences",
  adminView: AdminView = "controls",
): string {
  const url = new URL(currentHref);
  const encodedId = encodeURIComponent(id);
  url.pathname = `/sessions/${encodedId}${
    tab === "turns"
      ? `/turns${turnId ? `/${encodeURIComponent(turnId)}` : ""}`
      : tab === "settings"
        ? settingsPath(settingsTab, adminView)
        : tab === "help"
          ? "/help"
          : ""
  }`;
  url.search = "";
  url.hash = "";
  return url.toString();
}

export function buildHomeRouteUrl(
  currentHref: string,
  tab: HomeRouteTab = "chat",
  settingsTab: SettingsTab = "preferences",
  adminView: AdminView = "controls",
): string {
  const url = new URL(currentHref);
  url.pathname = `/new${
    tab === "settings"
      ? settingsPath(settingsTab, adminView)
      : tab === "help"
        ? "/help"
        : ""
  }`;
  url.search = "";
  url.hash = "";
  return url.toString();
}
