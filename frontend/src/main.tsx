import React from "react";
import ReactDOM from "react-dom/client";
import { App } from "./App";
import { LongChatDebugPage } from "./LongChatDebugPage";
import { SessionListDebugPage } from "./SessionListDebugPage";
import { AvatarPreviewHost } from "./avatarPreview";
import { noteUserScroll, startLongTaskObserver } from "./longTaskTelemetry";
import { StyleguideAvatars } from "./styleguide/avatars";
import { StyleguideBootState } from "./styleguide/boot-state";
import { StyleguideButtons } from "./styleguide/buttons";
import { StyleguideColors } from "./styleguide/colors";
import { StyleguideErrorPill } from "./styleguide/error-pill";
import { StyleguideIndex } from "./styleguide/index";
import { StyleguideMcpIcon } from "./styleguide/mcp-icon";
import { StyleguideModeChip } from "./styleguide/mode-chip";
import { StyleguideModeDropdown } from "./styleguide/mode-dropdown";
import { StyleguideNewSessionRow } from "./styleguide/new-session-row";
import { StyleguidePortfolioOnboarding } from "./styleguide/portfolio-onboarding";
import { StyleguidePortfolioTranscript } from "./styleguide/portfolio-transcript";
import { StyleguidePortfolioWorkspace } from "./styleguide/portfolio-workspace";
import { StyleguideRunHeaderTabs } from "./styleguide/run-header-tabs";
import { StyleguideMobileShell } from "./styleguide/mobile-shell";
import { StyleguideSessionRow } from "./styleguide/session-row";
import { StyleguideSpacing } from "./styleguide/spacing";
import { StyleguideStatusDot } from "./styleguide/status-dot";
import { StyleguideToolIcons } from "./styleguide/tool-icons";
import { StyleguideType } from "./styleguide/type";
import { StyleguideWelcomeCard } from "./styleguide/welcome-card";
import "./fonts.css";
import "./index.css";

if (typeof document !== "undefined") {
  document.documentElement.classList.add("dark");
  document.documentElement.style.colorScheme = "dark";
}

// Allowlist-based reap of stale localStorage keys in our namespace.
//
// Generalized version of the original `tank-run-entries-*` reap: any
// key whose name starts with `tank-` or `tank.` but isn't in the
// known-good prefix list gets dropped on boot. This is how we close
// the "stale key from a previous app version breaks the new version"
// class of bug — when a writer is removed (#384 took out the transcript
// cache writer, but #390 found 5MB of pre-removal keys still sitting
// in users' localStorage), the keys reap themselves on next load.
//
// To add a new owned key/prefix: append to TANK_KEY_ALLOWLIST below.
// Anything in our namespace not listed is treated as debris.
// Non-tank keys are left alone — other libs/sites share this origin.
const TANK_KEY_ALLOWLIST = [
  "tank-run-pref-",         // run-pane prefs (App.tsx)
  "tank.defaultSessionMode",
  "tank.defaultInteraction",
  "tank.homeSelectedRepos",
  "tank.sessionInteraction:",
];
function isAllowedTankKey(key: string): boolean {
  for (const allowed of TANK_KEY_ALLOWLIST) {
    if (key === allowed || key.startsWith(allowed)) return true;
  }
  return false;
}
if (typeof window !== "undefined") {
  try {
    for (const key of Object.keys(window.localStorage)) {
      const isTankKey = key.startsWith("tank-") || key.startsWith("tank.");
      if (isTankKey && !isAllowedTankKey(key)) {
        window.localStorage.removeItem(key);
      }
    }
  } catch {
    // localStorage can be unavailable in hardened/private contexts.
  }
}

// Tiny path-based routing. The styleguide ecosystem has a few routes:
// `/_styleguide` is the catalog/landing page; `/_styleguide/<feature>`
// is a per-feature focused page (e.g. /avatars for the agent avatar
// pool — stateful pickers want their own scroll context and viewport).
// Anything else falls through to the main app. No react-router; the
// branching is shallow enough to read inline.
const STYLEGUIDE_ROUTES: Record<string, () => JSX.Element> = {
  "/_styleguide": () => <StyleguideIndex />,
  "/_styleguide/colors": () => <StyleguideColors />,
  "/_styleguide/type": () => <StyleguideType />,
  "/_styleguide/spacing": () => <StyleguideSpacing />,
  "/_styleguide/buttons": () => <StyleguideButtons />,
  "/_styleguide/new-session-row": () => <StyleguideNewSessionRow />,
  "/_styleguide/status-dot": () => <StyleguideStatusDot />,
  "/_styleguide/mode-chip": () => <StyleguideModeChip />,
  "/_styleguide/tool-icons": () => <StyleguideToolIcons />,
  "/_styleguide/mcp-icon": () => <StyleguideMcpIcon />,
  "/_styleguide/run-header-tabs": () => <StyleguideRunHeaderTabs />,
  "/_styleguide/session-row": () => <StyleguideSessionRow />,
  "/_styleguide/mobile-shell": () => <StyleguideMobileShell />,
  "/_styleguide/mode-dropdown": () => <StyleguideModeDropdown />,
  "/_styleguide/welcome-card": () => <StyleguideWelcomeCard />,
  "/_styleguide/error-pill": () => <StyleguideErrorPill />,
  "/_styleguide/portfolio-workspace": () => <StyleguidePortfolioWorkspace />,
  "/_styleguide/portfolio-onboarding": () => <StyleguidePortfolioOnboarding />,
  "/_styleguide/portfolio-transcript": () => <StyleguidePortfolioTranscript />,
  "/_styleguide/boot-state": () => <StyleguideBootState />,
  "/_styleguide/avatars": () => <StyleguideAvatars />,
};

const DEBUG_ROUTES: Record<string, () => JSX.Element> = {
  "/_debug/long-chat": () => <LongChatDebugPage />,
  "/_debug/session-list": () => <SessionListDebugPage />,
};

function Root() {
  if (typeof window !== "undefined") {
    const debugRender = DEBUG_ROUTES[window.location.pathname];
    if (debugRender) return debugRender();
    const render = STYLEGUIDE_ROUTES[window.location.pathname];
    if (render) return render();
  }
  return <App />;
}

// Install the main-thread long-task observer before React mounts so the
// initial-paint chunk is also counted. The probe degrades silently on
// browsers without PerformanceObserver longtask support (Firefox).
startLongTaskObserver();
// Passive document-level scroll listener feeds the long-task probe's
// scroll-correlation signal. Capture=true catches scroll events on
// any scrollable container (chat transcript, sidebar) without each one
// needing its own listener. Passive so the listener can never block
// the input being measured.
if (typeof document !== "undefined") {
  document.addEventListener("scroll", noteUserScroll, { capture: true, passive: true });
}

ReactDOM.createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <Root />
    <AvatarPreviewHost />
  </React.StrictMode>,
);
