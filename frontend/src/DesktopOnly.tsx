import type { ReactNode } from "react"

import { useViewport } from "./useViewport"

export interface DesktopOnlyProps {
  /** Short surface name, e.g. "terminal sessions" or "the avatar manager". */
  feature: string
  /** Optional override for the explanatory line. */
  detail?: string
  /** The desktop/tablet surface to render above the compact breakpoint. */
  children: ReactNode
}

// A deliberate, documented product boundary — not a fallback. A few surfaces
// (terminal sessions, the admin avatar manager, internal debug pages) are
// genuinely unusable on a phone, so at <= BP_COMPACT we render an honest "open on
// a larger screen" card instead of a broken surface. This is the compact
// counterpart to the desktop-first operator console; see docs/design-system.md
// -> "Compact / mobile posture" and the mobile-session-triage capability ledger
// (docs/features/app-chrome/capabilities.md).
export function DesktopOnly({ feature, detail, children }: DesktopOnlyProps) {
  const { isCompact } = useViewport()
  if (!isCompact) return <>{children}</>
  return (
    <div className="desktop-only" role="note">
      <div className="desktop-only-inner">
        <p className="desktop-only-title">{feature} is desktop-only</p>
        <p className="desktop-only-body">
          {detail ?? `open this on a larger screen to use ${feature}.`}
        </p>
      </div>
    </div>
  )
}
