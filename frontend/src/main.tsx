import React from "react";
import ReactDOM from "react-dom/client";
import { App } from "./App";
import { StyleguideAvatars } from "./StyleguideAvatars";
import { StyleguideView } from "./StyleguideView";
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
  "tank-operator-jwt",      // session JWT (auth.ts)
  "tank-run-pref-",         // run-pane prefs (App.tsx)
  "tank.defaultSessionMode",
  "tank.defaultInteraction",
  "tank.sessionInteraction:",
  "tank.sessionOrder",      // matches tank.sessionOrder.<sub>
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
function Root() {
  if (typeof window !== "undefined") {
    const path = window.location.pathname;
    if (path === "/_styleguide") return <StyleguideView />;
    if (path === "/_styleguide/avatars") return <StyleguideAvatars />;
  }
  return <App />;
}

ReactDOM.createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <Root />
  </React.StrictMode>,
);
