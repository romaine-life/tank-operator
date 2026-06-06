import { useSyncExternalStore } from "react";

import { MQ_COMPACT, MQ_PHONE } from "./breakpoints";

// The single viewport-detection primitive in the app. Everything that branches
// on "phone-sized" reads it here so the breakpoint stays one source of truth
// (breakpoints.ts). Backed by matchMedia through useSyncExternalStore so React
// concurrent rendering always observes a tear-free snapshot.
//
// SSR/test-safe: when matchMedia is unavailable (node, older engines) every
// query reads false, i.e. the app renders its desktop shell. Compact is an
// additive client capability, never the server-assumed default.

export function readMatch(query: string): boolean {
  if (typeof window === "undefined" || typeof window.matchMedia !== "function") {
    return false;
  }
  return window.matchMedia(query).matches;
}

export function subscribeMatch(query: string, onChange: () => void): () => void {
  if (typeof window === "undefined" || typeof window.matchMedia !== "function") {
    return () => {};
  }
  const mql = window.matchMedia(query);
  // Safari <14 and some embedded webviews only expose the legacy listener API.
  if (typeof mql.addEventListener === "function") {
    mql.addEventListener("change", onChange);
    return () => mql.removeEventListener("change", onChange);
  }
  mql.addListener(onChange);
  return () => mql.removeListener(onChange);
}

// Module-scoped so identities stay stable across renders — a changing subscribe
// identity would make useSyncExternalStore resubscribe every render.
const subscribeCompact = (onChange: () => void) => subscribeMatch(MQ_COMPACT, onChange);
const subscribePhone = (onChange: () => void) => subscribeMatch(MQ_PHONE, onChange);
const readCompact = () => readMatch(MQ_COMPACT);
const readPhone = () => readMatch(MQ_PHONE);
const serverFalse = () => false;

export interface Viewport {
  /** Viewport <= BP_COMPACT: shell is single-column with an off-canvas drawer. */
  isCompact: boolean;
  /** Viewport <= BP_PHONE: densest phone tuning. */
  isPhone: boolean;
}

export function useViewport(): Viewport {
  const isCompact = useSyncExternalStore(subscribeCompact, readCompact, serverFalse);
  const isPhone = useSyncExternalStore(subscribePhone, readPhone, serverFalse);
  return { isCompact, isPhone };
}
