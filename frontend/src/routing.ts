export type RunTab = "chat" | "turns" | "background" | "files" | "settings" | "help";
export type HomeTab = "chat" | "settings" | "help";
export type AdminView = "controls" | "avatars" | "report" | "observability";
export type SettingsTab = "preferences" | "admin";

export type SessionRoute = {
  sessionId: string;
  tab: RunTab;
  turnId: string | null;
  settingsTab?: SettingsTab;
  adminView?: AdminView;
};

export type HomeRoute = {
  tab: HomeTab;
};

export type PublicMessageLinkRoute = {
  token: string;
  sessionId: string | null;
  messageId: string | null;
};

function decodeRouteSegment(segment: string): string {
  try {
    return decodeURIComponent(segment);
  } catch {
    return "";
  }
}

export function readSessionRouteFromPath(pathname = window.location.pathname): SessionRoute | null {
  const parts = pathname.split("/").filter(Boolean).map(decodeRouteSegment);
  if (parts[0] !== "sessions" || !parts[1]) return null;
  
  const sessionId = parts[1];
  const tabPart = parts[2];

  if (tabPart === "turns") {
    return { sessionId, tab: "turns", turnId: parts[3]?.trim() || null };
  }

  if (tabPart === "settings") {
    const settingsTab = parts[3] === "admin" ? "admin" : "preferences";
    const adminView = settingsTab === "admin" ? (parts[4] as AdminView || "controls") : undefined;
    return { sessionId, tab: "settings", turnId: null, settingsTab, adminView };
  }

  if (tabPart === "help" || tabPart === "files" || tabPart === "background") {
    return { sessionId, tab: tabPart as RunTab, turnId: null };
  }

  return { sessionId, tab: "chat", turnId: null };
}

export function readHomeRouteFromPath(pathname = window.location.pathname): HomeRoute | null {
  const parts = pathname.split("/").filter(Boolean).map(decodeRouteSegment);
  if (parts.length === 0) return { tab: "chat" };
  if (parts[0] === "settings") return { tab: "settings" };
  if (parts[0] === "help") return { tab: "help" };
  if (parts[0] === "sessions") return null; // Handled by session route
  return { tab: "chat" };
}

export function sessionRouteUrl(
  id: string, 
  tab: RunTab = "chat", 
  options: { 
    turnId?: string | null; 
    settingsTab?: SettingsTab; 
    adminView?: AdminView; 
  } = {}
): string {
  const url = new URL(window.location.href);
  let path = `/sessions/${encodeURIComponent(id)}`;
  
  if (tab === "turns") {
    path += `/turns${options.turnId ? `/${encodeURIComponent(options.turnId)}` : ""}`;
  } else if (tab === "settings") {
    path += "/settings";
    if (options.settingsTab === "admin") {
      path += "/admin";
      if (options.adminView && options.adminView !== "controls") {
        path += `/${encodeURIComponent(options.adminView)}`;
      }
    }
  } else if (tab !== "chat") {
    path += `/${tab}`;
  }

  url.pathname = path;
  url.search = "";
  url.hash = "";
  return url.toString();
}

export function homeRouteUrl(tab: HomeTab = "chat"): string {
  const url = new URL(window.location.href);
  url.pathname = tab === "chat" ? "/" : `/${tab}`;
  url.search = "";
  url.hash = "";
  return url.toString();
}

export function routeHasMessageTarget(): boolean {
  const params = new URLSearchParams(window.location.search);
  return params.has("message") || params.has("timeline_id");
}

export function readInitialPublicMessageLinkRoute(): PublicMessageLinkRoute | null {
  const params = new URLSearchParams(window.location.search);
  const token = (params.get("share") ?? "").trim();
  if (!token) return null;
  return {
    token,
    sessionId: params.get("session"),
    messageId: params.get("message") ?? params.get("timeline_id"),
  };
}
