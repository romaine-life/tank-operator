import type { ReactNode } from "react"

export interface MobileTopBarProps {
  /** True on the home / new-session surface (no active session). */
  isHome: boolean
  /** Active session name, shown as the bar's context label. */
  sessionName?: string
  /** Pre-rendered session avatar (lookup stays in App so it has one home). */
  avatar?: ReactNode
  /** Status dot class for the active session (live/idle/failed/...). */
  statusDotClass?: string
  /** Human label behind the status dot, for a11y + tooltip. */
  statusLabel?: string
  /** Open the off-canvas session drawer. */
  onOpenNav: () => void
}

// The compact-shell top bar. It exists only at <= BP_COMPACT, where the sidebar
// lives off-canvas and its collapse toggle is therefore unreachable. The bar
// exposes the single drawer trigger plus the current work context (which session
// you're in, and its live status) so orientation survives the sidebar being
// hidden — the App Chrome contract's "help the user orient ... without hiding
// product-critical state". Both the hamburger and the context label open the
// drawer, giving a forgiving touch target.
export function MobileTopBar({
  isHome,
  sessionName,
  avatar,
  statusDotClass,
  statusLabel,
  onOpenNav,
}: MobileTopBarProps) {
  return (
    <header className="mobile-topbar">
      <button
        type="button"
        className="mobile-topbar-nav"
        onClick={onOpenNav}
        aria-label="Open sessions"
        title="sessions"
      >
        <svg
          width="18"
          height="18"
          viewBox="0 0 18 18"
          aria-hidden="true"
          focusable="false"
        >
          <path
            d="M2.5 4.5h13M2.5 9h13M2.5 13.5h13"
            stroke="currentColor"
            strokeWidth="1.5"
            strokeLinecap="round"
          />
        </svg>
      </button>
      <button
        type="button"
        className="mobile-topbar-context"
        onClick={onOpenNav}
        aria-label="Open sessions"
      >
        {isHome ? (
          <span className="mobile-topbar-brand">tank-operator</span>
        ) : (
          <>
            {avatar}
            <span className="mobile-topbar-name">{sessionName}</span>
            {statusDotClass && (
              <span
                className={statusDotClass}
                title={statusLabel}
                aria-label={statusLabel ? `status: ${statusLabel}` : undefined}
              />
            )}
          </>
        )}
      </button>
    </header>
  )
}
