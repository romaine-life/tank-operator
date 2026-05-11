import React from "react";
import ReactDOM from "react-dom/client";
import { App } from "./App";
import { StyleguideView } from "./StyleguideView";
import "./fonts.css";
import "./index.css";

if (typeof document !== "undefined") {
  document.documentElement.classList.add("dark");
  document.documentElement.style.colorScheme = "dark";
}

// One-shot reap of pre-Phase-0 localStorage debris. The `tank-run-entries-*`
// writer was removed in #384 (the transcript cache moved to the backend
// replay paths) but the existing keys were left to rot — they accumulated
// per-session, ate the 5MB quota, and broke JWT writes with a confusing
// "exceeded the quota" auth error. Cheap to run on every boot; the
// startsWith filter is a no-op once the keys are gone.
if (typeof window !== "undefined") {
  try {
    for (const key of Object.keys(window.localStorage)) {
      if (key.startsWith("tank-run-entries-")) {
        window.localStorage.removeItem(key);
      }
    }
  } catch {
    // localStorage can be unavailable in hardened/private contexts.
  }
}

// Tiny path-based routing — the only route we mount that isn't App is
// the glimmung styleguide pilot's /_styleguide visual catalog. Avoids
// pulling in react-router for this single split.
function Root() {
  if (typeof window !== "undefined" && window.location.pathname === "/_styleguide") {
    return <StyleguideView />;
  }
  return <App />;
}

ReactDOM.createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <Root />
  </React.StrictMode>,
);
