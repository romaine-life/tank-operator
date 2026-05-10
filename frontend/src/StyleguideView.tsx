// Visual catalog of the components shipped by tank-operator's frontend.
// Mounted by App.tsx at /_styleguide. Reviewers + the screenshot pass
// get one URL to scan instead of synthesizing a feel from a diff.
//
// Contract: nelsong6/glimmung/docs/styleguide-contract.md.
//
// When you change a component (button voice, status dot, mode chip,
// session row layout), update its entry here in the same PR. There's
// no automated drift check — the env-prep phase's /_styleguide curl
// is the floor that catches "the route doesn't even render anymore",
// not "the styleguide drifted from the live UI."

import { useState } from "react";
import { ProviderIcon } from "./providerIcons";

const MODES = ["claude_cli", "api_key", "config", "codex_cli"] as const;
const MODE_LABELS: Record<(typeof MODES)[number], string> = {
  claude_cli: "claude-cli",
  api_key: "api",
  config: "config",
  codex_cli: "codex-cli",
};
const MODE_FULL_LABELS: Record<(typeof MODES)[number], string> = {
  claude_cli: "Claude CLI",
  api_key: "Claude API key",
  config: "Claude config",
  codex_cli: "Codex CLI",
};
const MODE_ICONS: Partial<Record<(typeof MODES)[number], "anthropic" | "codex">> = {
  claude_cli: "anthropic",
  codex_cli: "codex",
};
const STATUSES = ["active", "pending", "error"] as const;

function TankIcon({ className }: { className?: string }) {
  return (
    <svg
      className={className}
      viewBox="0 0 64 64"
      fill="none"
      stroke="currentColor"
      strokeWidth="2.5"
      strokeLinecap="round"
      strokeLinejoin="round"
      focusable="false"
      aria-hidden="true"
    >
      <rect x="8" y="28" width="40" height="14" rx="3" />
      <circle cx="16" cy="46" r="5" />
      <circle cx="40" cy="46" r="5" />
      <line x1="48" y1="32" x2="58" y2="32" />
      <rect x="22" y="20" width="14" height="8" rx="1.5" />
    </svg>
  );
}

const sectionStyle: React.CSSProperties = {
  padding: "32px 0 24px",
  borderTop: "1px solid var(--border-subtle)",
};
const headStyle: React.CSSProperties = {
  fontSize: "var(--text-xs)",
  textTransform: "uppercase",
  letterSpacing: "1.2px",
  color: "var(--text-muted)",
  margin: "0 0 4px 0",
};
const captionStyle: React.CSSProperties = {
  fontSize: "var(--text-xs)",
  color: "var(--text-faint)",
  margin: "0 0 14px 0",
  maxWidth: "60ch",
};
const rowStyle: React.CSSProperties = {
  display: "flex",
  flexWrap: "wrap",
  gap: "12px",
  alignItems: "center",
};

function IconWrench({ className }: { className?: string }) {
  return (
    <svg
      className={className}
      viewBox="0 0 24 24"
      width="16"
      height="16"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      focusable="false"
      aria-hidden="true"
    >
      <path d="M14.7 6.3a1 1 0 0 0 0 1.4l1.6 1.6a1 1 0 0 0 1.4 0l3.8-3.8a6 6 0 0 1-7.9 7.9l-6.9 6.9a2.1 2.1 0 0 1-3-3l6.9-6.9a6 6 0 0 1 7.9-7.9l-3.8 3.8Z" />
    </svg>
  );
}

function IconKey({ className }: { className?: string }) {
  return (
    <svg
      className={className}
      viewBox="0 0 16 16"
      width="16"
      height="16"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.7"
      strokeLinecap="round"
      strokeLinejoin="round"
      focusable="false"
      aria-hidden="true"
    >
      <circle cx="5.25" cy="8" r="2.6" />
      <path d="M7.85 8h5.15" />
      <path d="M11 8v2" />
      <path d="M13 8v2.2" />
    </svg>
  );
}

function IconChevronDown({ className }: { className?: string }) {
  return (
    <svg
      className={className}
      viewBox="0 0 16 16"
      width="12"
      height="12"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      focusable="false"
      aria-hidden="true"
    >
      <polyline points="4,6 8,10 12,6" />
    </svg>
  );
}

export function StyleguideView() {
  const [dropdownOpen, setDropdownOpen] = useState(true);

  return (
    <div
      style={{
        minHeight: "100vh",
        background: "var(--bg-app)",
        color: "var(--text-body)",
        fontFamily: "var(--font-primary)",
        padding: "32px 48px 64px",
      }}
    >
      <div style={{ maxWidth: 880 }}>
        <h1
          style={{
            fontSize: "var(--text-xs)",
            fontWeight: 400,
            color: "var(--text-muted)",
            textTransform: "uppercase",
            letterSpacing: "1.4px",
            margin: "0 0 4px 0",
          }}
        >
          tank-operator — styleguide
        </h1>
        <p style={{ ...captionStyle, marginBottom: 24 }}>
          Visual catalog of components shipped by tank-operator's React app.
          Reviewers get this URL alongside every PR; agents update it whenever
          a component changes. See <code>docs/styleguide-contract.md</code> in
          the glimmung repo for the contract.
        </p>

        {/* === buttons === */}
        <section style={sectionStyle}>
          <h2 style={headStyle}>buttons</h2>
          <p style={captionStyle}>
            Three voices: <code>btn-primary</code> for the dominant action on a
            screen (sign-in, install CTA), <code>btn-secondary</code> for
            recoveries (retry, cancel), <code>link-button</code> for inline
            text affordances. Disabled state is muted opacity, not a separate
            class.
          </p>
          <div style={rowStyle}>
            <button className="btn-primary">Sign in</button>
            <button className="btn-primary" disabled>Sign in</button>
            <button className="btn-secondary">retry</button>
            <button className="btn-secondary" disabled>retry</button>
            <button className="link-button">Sign out</button>
          </div>
        </section>

        {/* === new session row === */}
        <section style={sectionStyle}>
          <h2 style={headStyle}>new session row</h2>
          <p style={captionStyle}>
            Provider selector first, then default session, API-key fallback,
            and provider-specific config.
          </p>
          <div className="new-row new-row-launcher" data-menu="mode">
            <button className="new-row-provider-toggle" type="button" aria-label="choose provider">
              <span className="new-row-provider-slot">
                <ProviderIcon provider="anthropic" className="new-row-provider-icon" />
              </span>
              <IconChevronDown className="new-row-provider-chevron" />
            </button>
            <div className="new-row-action-group" role="group" aria-label="session actions">
              <button className="new-row-action" type="button" aria-label="start default session">
                <span className="row-icon">+</span>
              </button>
              <button className="new-row-action" type="button" aria-label="start API key session">
                <IconKey className="new-row-action-icon" />
              </button>
              <button className="new-row-action" type="button" aria-label="start config session">
                <IconWrench className="new-row-action-icon" />
              </button>
            </div>
          </div>
        </section>

        {/* === status dots === */}
        <section style={sectionStyle}>
          <h2 style={headStyle}>status dot</h2>
          <p style={captionStyle}>
            Replaces the old text pill ("Active" / "Pending" / "Failed") in
            the session row. Color carries the status; shape stays neutral so
            the row's dominant visual belongs to the session name.
          </p>
          <div style={rowStyle}>
            {STATUSES.map((s) => (
              <div key={s} style={{ display: "flex", alignItems: "center", gap: 8 }}>
                <span className={`status-dot status-${s}`} aria-label={`status ${s}`} />
                <span style={{ fontSize: "var(--text-xs)", color: "var(--text-muted)" }}>{s}</span>
              </div>
            ))}
          </div>
        </section>

        {/* === mode chips === */}
        <section style={sectionStyle}>
          <h2 style={headStyle}>mode chip</h2>
          <p style={captionStyle}>
            Surfaces the auth mode (Claude / API / config) on the
            session row. Each rides its own tinted background — not bordered
            pills — so the row is calm at rest but legible at a glance.
          </p>
          <div style={rowStyle}>
            {MODES.map((m) => (
              <span
                key={m}
                className={`mode mode-${m}${MODE_ICONS[m] ? " mode-icon-only" : ""}`}
                title={MODE_FULL_LABELS[m]}
                aria-label={MODE_FULL_LABELS[m]}
              >
                {MODE_ICONS[m] ? (
                  <>
                    <ProviderIcon provider={MODE_ICONS[m]} className="mode-provider-icon" />
                    <span className="sr-only">{MODE_LABELS[m]}</span>
                  </>
                ) : (
                  MODE_LABELS[m]
                )}
              </span>
            ))}
          </div>
        </section>

        {/* === session row === */}
        <section style={sectionStyle}>
          <h2 style={headStyle}>session row</h2>
          <p style={captionStyle}>
            One row per session in the sidebar list. Top: status dot + session
            name + compact boot/runtime stats + delete affordance. Bottom: mode chip +
            optional inline actions (remote-control, rollout, save-credentials). Active
            row gets the <code>is-open</code> class; not styled here for brevity.
          </p>
          <ul className="sessions" style={{ maxWidth: 360, listStyle: "none", padding: 0, margin: 0 }}>
            <li>
              <div className="session-row-top">
                <span className="status-dot status-active" aria-label="status active" />
                <button className="session-open" type="button">
                  <span className="session-id">my-session</span>
                </button>
                <span className="session-stats">
                  <span className="session-stat" title="ready 32s after request">
                    <span aria-hidden="true">↓</span>
                    <span>32s</span>
                  </span>
                  <span className="session-stat" title="running 12m">
                    <span aria-hidden="true">↑</span>
                    <span>12m</span>
                  </span>
                </span>
                <button className="session-delete" aria-label="delete session" type="button">
                  ×
                </button>
              </div>
              <div className="session-row-bottom">
                <span className="mode mode-claude_cli mode-icon-only" title="Claude CLI" aria-label="Claude CLI">
                  <ProviderIcon provider="anthropic" className="mode-provider-icon" />
                  <span className="sr-only">claude-cli</span>
                </span>
                <button className="session-action session-remote is-icon" type="button" aria-label="remote control">
                  <span>↗</span>
                </button>
                <button className="session-action session-rollout is-icon" type="button" aria-label="start rollout">
                  <TankIcon className="session-action-tank-icon" />
                </button>
                <button className="session-action session-rollout is-icon is-clicked" type="button" aria-label="start rollout">
                  <TankIcon className="session-action-tank-icon" />
                </button>
              </div>
            </li>
            <li>
              <div className="session-row-top">
                <span className="status-dot status-pending" aria-label="status pending" />
                <button className="session-open" type="button">
                  <span className="session-id">starting…</span>
                </button>
                <span className="session-stats">
                  <span className="session-stat" title="starting for 18s since request">
                    <span aria-hidden="true">↓</span>
                    <span>18s</span>
                  </span>
                  <span className="session-stat" title="running less than 1m">
                    <span aria-hidden="true">↑</span>
                    <span>&lt;1m</span>
                  </span>
                </span>
                <button className="session-delete" aria-label="delete session" type="button">
                  ×
                </button>
              </div>
              <div className="session-row-bottom">
                <span className="mode mode-api_key">api</span>
              </div>
            </li>
          </ul>
        </section>

        {/* === dropdown === */}
        <section style={sectionStyle}>
          <h2 style={headStyle}>mode dropdown</h2>
          <p style={captionStyle}>
            Provider selection is the only dropdown; action icons stay in the
            launcher row.
          </p>
          <div className="new-row new-row-launcher" data-menu="mode">
            <button
              className={`new-row-provider-toggle${dropdownOpen ? " is-open" : ""}`}
              type="button"
              aria-label="choose provider"
              onClick={() => setDropdownOpen((v) => !v)}
            >
              <span className="new-row-provider-slot">
                <ProviderIcon provider="anthropic" className="new-row-provider-icon" />
              </span>
              <IconChevronDown className="new-row-provider-chevron" />
            </button>
            <div className="new-row-action-group" role="group" aria-label="session actions">
              <button className="new-row-action" type="button" aria-label="start default session">
                <span className="row-icon">+</span>
              </button>
              <button className="new-row-action" type="button" aria-label="start API key session">
                <IconKey className="new-row-action-icon" />
              </button>
              <button className="new-row-action" type="button" aria-label="start config session">
                <IconWrench className="new-row-action-icon" />
              </button>
            </div>
            {dropdownOpen && (
              <ul className="dropdown dropdown-provider" role="menu">
                <li>
                  <button type="button" aria-label="Claude">
                    <ProviderIcon provider="anthropic" className="dropdown-provider-icon" />
                    <span className="sr-only">Claude</span>
                  </button>
                </li>
                <li>
                  <button type="button" aria-label="Codex">
                    <ProviderIcon provider="codex" className="dropdown-provider-icon" />
                    <span className="sr-only">Codex</span>
                  </button>
                </li>
              </ul>
            )}
          </div>
        </section>

        {/* === welcome card === */}
        <section style={sectionStyle}>
          <h2 style={headStyle}>welcome card</h2>
          <p style={captionStyle}>
            The shape used for boot states (sign-in, GitHub install onboarding,
            auth error). Centered card with a primary CTA and an inline
            click-to-dismiss error pill rendered above when set.
          </p>
          <div style={{ background: "var(--bg-elevated)", border: "1px solid var(--border-strong)", borderRadius: "var(--radius-lg)", padding: 24, maxWidth: 480 }}>
            <div className="welcome-inner onboarding">
              <h2 className="welcome-title">Connect GitHub</h2>
              <p className="welcome-sub">
                tank-operator needs the App installed so it can run sessions
                against your repos. The next page is GitHub's; come back here
                after.
              </p>
              <a className="btn-primary onboarding-cta" href="#">Install</a>
              <p className="onboarding-meta">
                signed in as <code>you@example.com</code> ·{" "}
                <button className="link-button" type="button">Sign out</button>
              </p>
            </div>
          </div>
        </section>

        {/* === error === */}
        <section style={sectionStyle}>
          <h2 style={headStyle}>error pill</h2>
          <p style={captionStyle}>
            Inline error surface, click-to-dismiss. Used for transient failures
            (lost socket, save failed, install error) above the active card or
            in the sidebar.
          </p>
          <div style={rowStyle}>
            <pre className="error" style={{ margin: 0 }}>save failed: 500</pre>
          </div>
        </section>

        {/* === boot states === */}
        <section style={sectionStyle}>
          <h2 style={headStyle}>boot state</h2>
          <p style={captionStyle}>
            Full-screen states for app lifecycle (loading, sign-in needed, auth
            error). Renders in <code>div.boot-state</code> — single centered
            element + minimal chrome.
          </p>
          <div className="boot-state" style={{ minHeight: 80, padding: 16, border: "1px dashed var(--border-strong)", borderRadius: "var(--radius-md)" }}>
            <span className="boot-text">loading…</span>
          </div>
        </section>
      </div>
    </div>
  );
}
