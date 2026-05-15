// Visual catalog of the components shipped by tank-operator's frontend.
// Mounted by App.tsx at /_styleguide. Reviewers + the screenshot pass
// get one URL to scan instead of synthesizing a feel from a diff.
//
// Contract: nelsong6/glimmung/docs/styleguide-contract.md.
//
// When you change a component (button voice, status dot, mode chip,
// session row layout), update its entry here in the same PR. There's
// no automated drift check; the workflow's /_styleguide curl
// is the floor that catches "the route doesn't even render anymore",
// not "the styleguide drifted from the live UI."

import { useState } from "react";
import {
  ArrowLeftIcon,
  FolderIcon,
  InfoIcon,
  SettingsIcon,
  SquareTerminalIcon,
} from "lucide-react";
import { McpIcon } from "./McpIcon";
import { ProviderIcon } from "./providerIcons";
import { AgentAvatarIcon, getSessionAvatar } from "./sessionAvatars";

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
const SURFACE_SWATCHES = [
  ["app", "--bg-app", "#171717"],
  ["sidebar", "--bg-sidebar", "rgba(13,13,13,0.88)"],
  ["control", "--bg-sidebar-control", "rgba(255,255,255,0.03)"],
  ["hover", "--bg-sidebar-hover", "rgba(255,255,255,0.075)"],
  ["active", "--bg-sidebar-active", "rgba(79,140,247,0.10)"],
] as const;
const SEMANTIC_SWATCHES = [
  ["accent", "--accent-fg", "#b9d2fb"],
  ["remote", "--cyan", "#67e8f9"],
  ["online", "--status-online", "#34d399"],
  ["failed", "--status-error-fg", "#ef6f6f"],
  ["needs input", "--status-agent-needs-input", "#fb923c"],
] as const;
const TYPE_SAMPLES = [
  ["xs", "--text-xs", "12px", "sidebar meta and compact labels"],
  ["sm", "--text-sm", "14px", "default chrome and body"],
  ["base", "--text-base", "16px", "brand and larger controls"],
  ["lg", "--text-lg", "18px", "run header session title"],
  ["2xl", "--text-2xl", "24px", "onboarding titles"],
] as const;
const RADIUS_SAMPLES = [
  ["sm", "--radius-sm", "6px"],
  ["md", "--radius-md", "8px"],
  ["lg", "--radius-lg", "12px"],
  ["xl", "--radius-xl", "16px"],
  ["pill", "--radius-pill", "9999px"],
] as const;

type InspectBox = {
  top: number;
  left: number;
  width: number;
  height: number;
  label: string;
};

type DesignSelection = {
  url: string;
  path: string;
  selected_at: string;
  viewport: {
    width: number;
    height: number;
  };
  element: {
    tag: string;
    role: string | null;
    name: string | null;
    text: string;
    id: string | null;
    class_name: string | null;
    test_id: string | null;
    design_component: string | null;
    design_state: string | null;
    design_source: string | null;
  };
  specimen: {
    heading: string | null;
  };
  rect: {
    x: number;
    y: number;
    width: number;
    height: number;
  };
  selectors: {
    test_id: string | null;
    role: string | null;
    css_hint: string;
  };
  styles: {
    display: string;
    position: string;
    color: string;
    background_color: string;
    font_size: string;
  };
};

function readableElementName(el: HTMLElement) {
  const labelledBy = el.getAttribute("aria-labelledby");
  if (labelledBy) {
    const label = labelledBy
      .split(/\s+/)
      .map((id) => document.getElementById(id)?.textContent?.trim())
      .filter(Boolean)
      .join(" ");
    if (label) return label;
  }
  return (
    el.getAttribute("aria-label") ||
    el.getAttribute("title") ||
    el.textContent?.replace(/\s+/g, " ").trim() ||
    null
  );
}

function cssHint(el: HTMLElement) {
  if (el.id) return `#${CSS.escape(el.id)}`;
  const testId = el.dataset.testid;
  if (testId) return `[data-testid="${testId}"]`;
  const component = el.dataset.designComponent;
  if (component) return `[data-design-component="${component}"]`;
  const className = Array.from(el.classList).slice(0, 2).map((name) => `.${CSS.escape(name)}`).join("");
  return `${el.tagName.toLowerCase()}${className}`;
}

function nearestSpecimenHeading(el: HTMLElement, root: HTMLElement) {
  const section = el.closest("section");
  if (!section || !root.contains(section)) return null;
  return section.querySelector("h2")?.textContent?.replace(/\s+/g, " ").trim() || null;
}

function inspectBoxFor(el: HTMLElement): InspectBox {
  const rect = el.getBoundingClientRect();
  return {
    top: rect.top,
    left: rect.left,
    width: rect.width,
    height: rect.height,
    label: el.dataset.designComponent || el.getAttribute("aria-label") || el.tagName.toLowerCase(),
  };
}

function selectionForElement(el: HTMLElement, root: HTMLElement): DesignSelection {
  const rect = el.getBoundingClientRect();
  const styles = window.getComputedStyle(el);
  const name = readableElementName(el);
  const role = el.getAttribute("role") || (el instanceof HTMLButtonElement ? "button" : null);
  const testId = el.dataset.testid || null;

  return {
    url: window.location.href,
    path: window.location.pathname,
    selected_at: new Date().toISOString(),
    viewport: {
      width: window.innerWidth,
      height: window.innerHeight,
    },
    element: {
      tag: el.tagName.toLowerCase(),
      role,
      name,
      text: el.textContent?.replace(/\s+/g, " ").trim().slice(0, 160) || "",
      id: el.id || null,
      class_name: el.className || null,
      test_id: testId,
      design_component: el.dataset.designComponent || null,
      design_state: el.dataset.designState || null,
      design_source: el.dataset.designSource || null,
    },
    specimen: {
      heading: nearestSpecimenHeading(el, root),
    },
    rect: {
      x: Math.round(rect.x),
      y: Math.round(rect.y),
      width: Math.round(rect.width),
      height: Math.round(rect.height),
    },
    selectors: {
      test_id: testId ? `[data-testid="${testId}"]` : null,
      role: role && name ? `getByRole('${role}', { name: ${JSON.stringify(name)} })` : null,
      css_hint: cssHint(el),
    },
    styles: {
      display: styles.display,
      position: styles.position,
      color: styles.color,
      background_color: styles.backgroundColor,
      font_size: styles.fontSize,
    },
  };
}

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
const showcaseFrameStyle: React.CSSProperties = {
  border: "1px solid var(--border-soft)",
  borderRadius: "var(--radius-md)",
  background: "var(--bg-base)",
  overflow: "hidden",
};
const portfolioFrameStyle: React.CSSProperties = {
  ...showcaseFrameStyle,
  height: 420,
  overflowX: "auto",
};

function Swatch({ label, token, value }: { label: string; token: string; value: string }) {
  return (
    <div
      style={{
        display: "grid",
        gridTemplateColumns: "72px 1fr",
        gap: 10,
        alignItems: "center",
        minWidth: 260,
      }}
    >
      <span
        style={{
          height: 44,
          borderRadius: "var(--radius-md)",
          border: "1px solid var(--border-strong)",
          background: `var(${token})`,
        }}
      />
      <span style={{ display: "grid", gap: 2 }}>
        <span style={{ fontSize: "var(--text-sm)", color: "var(--text-primary)" }}>{label}</span>
        <code>{token}</code>
        <span style={{ fontSize: "var(--text-xs)", color: "var(--text-faint)" }}>{value}</span>
      </span>
    </div>
  );
}

function TypeSample({
  name,
  token,
  size,
  role,
}: {
  name: string;
  token: string;
  size: string;
  role: string;
}) {
  return (
    <div style={{ display: "grid", gap: 4, padding: "10px 0", borderTop: "1px solid var(--border-subtle)" }}>
      <span style={{ fontFamily: "var(--font-primary)", fontSize: `var(${token})`, color: "var(--text-primary)" }}>
        {role}
      </span>
      <span style={{ fontSize: "var(--text-xs)", color: "var(--text-faint)" }}>
        {name} · <code>{token}</code> · {size}
      </span>
    </div>
  );
}

function MiniTerminal() {
  return (
    <div
      aria-label="styleguide terminal sample"
      style={{
        flex: 1,
        minHeight: 0,
        padding: 18,
        background: "#171717",
        color: "var(--text-body)",
        fontFamily: "var(--font-mono)",
        fontSize: 13,
        lineHeight: 1.45,
        whiteSpace: "pre-wrap",
      }}
    >{` ▐▛███▜▌   Codex
▝▜█████▛▘  GPT-5.5 · /workspace
  ▘▘ ▝▝

$ rg "run-tab" frontend/src
frontend/src/App.tsx
frontend/src/StyleguideView.tsx

[reconnected]
❯ `}</div>
  );
}

function PortfolioWorkspaceScene() {
  return (
    <div className="shell" style={{ height: "100%", minWidth: 880, gridTemplateColumns: "260px 1fr" }}>
      <aside className="sidebar">
        <div className="sidebar-brand">
          <button className="sidebar-home is-active" type="button" aria-label="Home">
            <span className="sidebar-home-label">tank-operator</span>
          </button>
        </div>
        <div className="sidebar-section">
          <div className="new-row new-row-launcher">
            <button className="new-row-provider-toggle" type="button" aria-label="choose provider">
              <span className="new-row-provider-slot">
                <ProviderIcon provider="codex" className="new-row-provider-icon" />
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
        </div>
        <div className="sidebar-list">
          <div className="sidebar-section-label">Sessions</div>
          <ul className="sessions">
            <li className="is-open">
              <div className="session-row-top">
                <span className="status-dot status-active" aria-label="status active" />
                <button className="session-open" type="button">
                  <span className="session-id">design-showcase</span>
                </button>
                <button className="session-delete" aria-label="delete session" type="button">×</button>
              </div>
              <div className="session-row-bottom">
                <span className="mode mode-codex_cli mode-icon-only" title="Codex CLI" aria-label="Codex CLI">
                  <ProviderIcon provider="codex" className="mode-provider-icon" />
                  <span className="sr-only">codex-cli</span>
                </span>
                <button className="session-action session-remote is-icon" type="button" aria-label="remote control">
                  <span>↗</span>
                </button>
              </div>
            </li>
            <li>
              <div className="session-row-top">
                <span className="status-dot status-pending" aria-label="status pending" />
                <button className="session-open" type="button">
                  <span className="session-id">avatar-review</span>
                </button>
                <button className="session-delete" aria-label="delete session" type="button">×</button>
              </div>
              <div className="session-row-bottom">
                <span className="mode mode-claude_cli mode-icon-only" title="Claude CLI" aria-label="Claude CLI">
                  <ProviderIcon provider="anthropic" className="mode-provider-icon" />
                  <span className="sr-only">claude-cli</span>
                </span>
              </div>
            </li>
          </ul>
        </div>
      </aside>
      <section className="run-panel">
        <header className="run-header">
          <div className="run-header-title">
            <button className="run-header-name-btn" type="button">design-showcase</button>
          </div>
          <nav className="run-tabs" aria-label="Session actions">
            <button className="run-tab" type="button">
              <FolderIcon className="run-tab-icon" strokeWidth={1.8} aria-hidden="true" />
              <span>Files</span>
            </button>
            <button className="run-tab run-tab-active" type="button" aria-pressed={true}>
              <SettingsIcon className="run-tab-icon" aria-hidden="true" />
              <span>Settings</span>
            </button>
            <button className="run-tab" type="button">
              <InfoIcon className="run-tab-icon" aria-hidden="true" />
              <span>Help</span>
            </button>
          </nav>
        </header>
        <MiniTerminal />
      </section>
    </div>
  );
}

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
  const [inspectMode, setInspectMode] = useState(false);
  const [hoverBox, setHoverBox] = useState<InspectBox | null>(null);
  const [selectedBox, setSelectedBox] = useState<InspectBox | null>(null);
  const [selection, setSelection] = useState<DesignSelection | null>(null);
  const [selectionStatus, setSelectionStatus] = useState("no selection yet");

  function handleInspectMove(event: React.MouseEvent<HTMLDivElement>) {
    if (!inspectMode || !(event.target instanceof HTMLElement)) return;
    const target = event.target.closest<HTMLElement>("[data-inspectable], button, a, [role], code, pre, li, section");
    if (!target || target.closest("[data-inspector-control]")) {
      setHoverBox(null);
      return;
    }
    setHoverBox(inspectBoxFor(target));
  }

  function handleInspectClick(event: React.MouseEvent<HTMLDivElement>) {
    if (!inspectMode || !(event.target instanceof HTMLElement)) return;
    if (event.target.closest("[data-inspector-control]")) return;

    const target = event.target.closest<HTMLElement>("[data-inspectable], button, a, [role], code, pre, li, section");
    if (!target) return;

    event.preventDefault();
    event.stopPropagation();

    const nextSelection = selectionForElement(target, event.currentTarget);
    setSelection(nextSelection);
    setSelectedBox(inspectBoxFor(target));
    setSelectionStatus("posting selection...");

    void fetch("/api/design/selection", {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify(nextSelection),
    })
      .then((res) => {
        setSelectionStatus(res.ok ? "selection posted" : `post failed: ${res.status}`);
      })
      .catch((error: unknown) => {
        setSelectionStatus(error instanceof Error ? `post failed: ${error.message}` : "post failed");
      });
  }

  return (
    <div
      onMouseMove={handleInspectMove}
      onMouseLeave={() => setHoverBox(null)}
      onClickCapture={handleInspectClick}
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

        <section
          data-inspector-control
          style={{
            ...sectionStyle,
            position: "sticky",
            top: 0,
            zIndex: 30,
            background: "rgba(23,23,23,0.94)",
            backdropFilter: "blur(8px)",
          }}
        >
          <h2 style={headStyle}>select element</h2>
          <p style={captionStyle}>
            Toggle inspection, hover to confirm the target, then click a UI
            element. The styleguide posts a structured selection packet to
            <code> /api/design/selection</code>; agents can read it from
            <code> /api/design/selection/latest</code>.
          </p>
          <div style={{ display: "grid", gap: 12 }}>
            <div style={rowStyle}>
              <button
                className={inspectMode ? "btn-primary" : "btn-secondary"}
                type="button"
                onClick={() => {
                  setInspectMode((value) => !value);
                  setHoverBox(null);
                }}
              >
                {inspectMode ? "selecting..." : "Select element"}
              </button>
              <button
                className="link-button"
                type="button"
                onClick={() => {
                  setSelection(null);
                  setSelectedBox(null);
                  setSelectionStatus("no selection yet");
                }}
              >
                Clear
              </button>
              <span style={{ fontSize: "var(--text-xs)", color: "var(--text-faint)" }}>
                {selectionStatus}
              </span>
            </div>
            {selection && (
              <pre
                className="error"
                style={{
                  margin: 0,
                  maxHeight: 220,
                  overflow: "auto",
                  background: "var(--bg-base)",
                  color: "var(--text-body)",
                }}
              >
                {JSON.stringify(selection, null, 2)}
              </pre>
            )}
          </div>
        </section>

        {/* === foundations: colors === */}
        <section style={sectionStyle}>
          <h2 style={headStyle}>colors</h2>
          <p style={captionStyle}>
            Dark-only surfaces and small semantic accents. Hover states recess
            into darker fills; active/open states are lighter and easier to scan.
          </p>
          <div style={{ display: "grid", gap: 18 }}>
            <div style={rowStyle}>
              {SURFACE_SWATCHES.map(([label, token, value]) => (
                <Swatch key={token} label={label} token={token} value={value} />
              ))}
            </div>
            <div style={rowStyle}>
              {SEMANTIC_SWATCHES.map(([label, token, value]) => (
                <Swatch key={token} label={label} token={token} value={value} />
              ))}
            </div>
          </div>
        </section>

        {/* === foundations: type === */}
        <section style={sectionStyle}>
          <h2 style={headStyle}>type</h2>
          <p style={captionStyle}>
            Archivo carries chrome labels and controls. Mono is reserved for
            terminal output and literal code/path snippets.
          </p>
          <div style={{ display: "grid", gap: 0, maxWidth: 620 }}>
            {TYPE_SAMPLES.map(([name, token, size, role]) => (
              <TypeSample key={token} name={name} token={token} size={size} role={role} />
            ))}
            <div style={{ display: "grid", gap: 4, padding: "10px 0", borderTop: "1px solid var(--border-subtle)" }}>
              <span style={{ fontFamily: "var(--font-mono)", fontSize: "var(--text-sm)", color: "var(--text-body)" }}>
                /workspace/tank-operator/frontend/src/App.tsx
              </span>
              <span style={{ fontSize: "var(--text-xs)", color: "var(--text-faint)" }}>
                mono · <code>--font-mono</code> · terminal and literal paths only
              </span>
            </div>
          </div>
        </section>

        {/* === foundations: spacing === */}
        <section style={sectionStyle}>
          <h2 style={headStyle}>spacing and radii</h2>
          <p style={captionStyle}>
            Stable dimensions matter more than decorative depth. Use the radius
            ladder consistently so labels, icons, and hover states cannot shift
            nearby layout.
          </p>
          <div style={rowStyle}>
            {RADIUS_SAMPLES.map(([name, token, size]) => (
              <div key={token} style={{ display: "grid", gap: 8, width: 124 }}>
                <span
                  style={{
                    height: 54,
                    borderRadius: `var(${token})`,
                    background: "var(--bg-sidebar-control)",
                    border: "1px solid var(--row-rest-ring)",
                  }}
                />
                <span style={{ fontSize: "var(--text-xs)", color: "var(--text-muted)" }}>{name} · {size}</span>
                <code>{token}</code>
              </div>
            ))}
          </div>
        </section>

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

        {/* === Tool icons === */}
        <section style={sectionStyle}>
          <h2 style={headStyle}>tool icons</h2>
          <p style={captionStyle}>
            Transcript tool rows use semantic glyphs instead of relying on the
            rendered tool label.
          </p>
          <div style={rowStyle}>
            <span className="run-tool-icon-glyph tool-color-bash" aria-hidden="true">
              <SquareTerminalIcon size={14} strokeWidth={2} />
            </span>
            <span className="run-tool-icon-glyph tool-color-mcp" aria-hidden="true">
              <McpIcon size={14} strokeWidth={2} />
            </span>
          </div>
        </section>

        {/* === MCP icon === */}
        <section style={sectionStyle}>
          <h2 style={headStyle}>mcp icon</h2>
          <p style={captionStyle}>
            Used for MCP server controls and MCP tool calls. The glyph follows
            the Model Context Protocol mark and inherits the surrounding icon
            color.
          </p>
          <div style={rowStyle}>
            <button
              type="button"
              className="run-composer-icon-btn"
              aria-label="Show MCP servers"
              title="Show MCP servers"
            >
              <McpIcon className="run-composer-icon" aria-hidden="true" />
            </button>
            <span className="run-tool-icon-glyph tool-color-mcp" aria-hidden="true">
              <McpIcon size={14} strokeWidth={2} />
            </span>
          </div>
        </section>

        {/* === run header tabs === */}
        <section style={sectionStyle}>
          <h2 style={headStyle}>run header tabs</h2>
          <p style={captionStyle}>
            Header tabs that open side-pane views inside a session. The label
            text must stay aligned with the icon at desktop width and remain
            readable in the narrow horizontal-scroll state.
          </p>
          <div style={{ display: "grid", gap: 14 }}>
            <div style={showcaseFrameStyle}>
              <section className="run-panel" style={{ minHeight: 116 }}>
                <header className="run-header">
                  <div className="run-header-title">
                    <button className="run-header-name-btn" type="button">
                      avatar-dinosaur-pool
                    </button>
                  </div>
                  <nav className="run-tabs" aria-label="Session actions">
                    <button
                      className="run-tab"
                      type="button"
                      aria-pressed={false}
                      data-testid="styleguide-run-tab-files"
                      data-design-component="RunHeaderTab"
                      data-design-state="rest"
                      data-design-source="frontend/src/App.tsx"
                    >
                      <FolderIcon className="run-tab-icon" strokeWidth={1.8} aria-hidden="true" />
                      <span>Files</span>
                    </button>
                    <button
                      className="run-tab run-tab-active"
                      type="button"
                      aria-pressed={true}
                      data-testid="styleguide-run-tab-settings-active"
                      data-design-component="RunHeaderTab"
                      data-design-state="active"
                      data-design-source="frontend/src/App.tsx"
                    >
                      <SettingsIcon className="run-tab-icon" aria-hidden="true" />
                      <span>Settings</span>
                    </button>
                    <button className="run-tab" type="button" aria-pressed={false}>
                      <InfoIcon className="run-tab-icon" aria-hidden="true" />
                      <span>Help</span>
                    </button>
                  </nav>
                </header>
              </section>
            </div>
            <div style={showcaseFrameStyle}>
              <section className="run-panel" style={{ minHeight: 116 }}>
                <header className="run-header">
                  <div className="run-header-title">
                    <button className="run-header-name-btn" type="button">
                      session-with-files-open
                    </button>
                  </div>
                  <nav className="run-tabs" aria-label="Session actions">
                    <button
                      className="run-tab run-tab-back"
                      type="button"
                      data-testid="styleguide-run-tab-back"
                      data-design-component="RunHeaderTab"
                      data-design-state="side-pane-back"
                      data-design-source="frontend/src/App.tsx"
                    >
                      <ArrowLeftIcon className="run-tab-icon" strokeWidth={2.2} aria-hidden="true" />
                      <span>Back</span>
                    </button>
                    <button
                      className="run-tab run-tab-active"
                      type="button"
                      aria-pressed={true}
                      data-testid="styleguide-run-tab-files-active"
                      data-design-component="RunHeaderTab"
                      data-design-state="side-pane-open"
                      data-design-source="frontend/src/App.tsx"
                    >
                      <FolderIcon className="run-tab-icon" strokeWidth={1.8} aria-hidden="true" />
                      <span>Files</span>
                    </button>
                    <button className="run-tab" type="button" aria-pressed={false}>
                      <SettingsIcon className="run-tab-icon" aria-hidden="true" />
                      <span>Settings</span>
                    </button>
                    <button className="run-tab" type="button" aria-pressed={false}>
                      <InfoIcon className="run-tab-icon" aria-hidden="true" />
                      <span>Help</span>
                    </button>
                  </nav>
                </header>
              </section>
            </div>
            <div style={{ ...showcaseFrameStyle, maxWidth: 390 }}>
              <section className="run-panel" style={{ minHeight: 142 }}>
                <header className="run-header">
                  <div className="run-header-title">
                    <button className="run-header-name-btn" type="button">
                      narrow-session
                    </button>
                  </div>
                  <nav className="run-tabs" aria-label="Session actions">
                    <button
                      className="run-tab run-tab-back"
                      type="button"
                      data-testid="styleguide-run-tab-back-narrow"
                      data-design-component="RunHeaderTab"
                      data-design-state="narrow-side-pane-back"
                      data-design-source="frontend/src/App.tsx"
                    >
                      <ArrowLeftIcon className="run-tab-icon" strokeWidth={2.2} aria-hidden="true" />
                      <span>Back</span>
                    </button>
                    <button
                      className="run-tab run-tab-active"
                      type="button"
                      aria-pressed={true}
                      data-testid="styleguide-run-tab-files-narrow-active"
                      data-design-component="RunHeaderTab"
                      data-design-state="narrow-side-pane-open"
                      data-design-source="frontend/src/App.tsx"
                    >
                      <FolderIcon className="run-tab-icon" strokeWidth={1.8} aria-hidden="true" />
                      <span>Files</span>
                    </button>
                    <button className="run-tab" type="button" aria-pressed={false}>
                      <SettingsIcon className="run-tab-icon" aria-hidden="true" />
                      <span>Settings</span>
                    </button>
                    <button className="run-tab" type="button" aria-pressed={false}>
                      <InfoIcon className="run-tab-icon" aria-hidden="true" />
                      <span>Help</span>
                    </button>
                  </nav>
                </header>
              </section>
            </div>
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
              <AgentAvatarIcon avatar={getSessionAvatar("my-session")} className="session-avatar" />
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
              <AgentAvatarIcon avatar={getSessionAvatar("starting")} className="session-avatar" />
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

        {/* === portfolio scenes === */}
        <section style={sectionStyle}>
          <h2 style={headStyle}>portfolio scene: session workspace</h2>
          <p style={captionStyle}>
            Full shell composition for reviewing density, sidebar hierarchy,
            run header tabs, and terminal contrast together.
          </p>
          <div style={portfolioFrameStyle}>
            <PortfolioWorkspaceScene />
          </div>
        </section>

        <section style={sectionStyle}>
          <h2 style={headStyle}>portfolio scene: onboarding</h2>
          <p style={captionStyle}>
            First-run wall. This stays sparse: one task, one primary CTA,
            diagnostic supporting copy.
          </p>
          <div
            style={{
              ...showcaseFrameStyle,
              minHeight: 300,
              display: "grid",
              placeItems: "center",
              padding: 24,
            }}
          >
            <div className="welcome-inner onboarding" style={{ maxWidth: 460 }}>
              <h2 className="welcome-title">Connect GitHub</h2>
              <p className="welcome-sub">
                tank-operator needs the App installed so sessions can read and
                write repos through mcp-github.
              </p>
              <a className="btn-primary onboarding-cta" href="#">Install GitHub App</a>
              <p className="onboarding-meta">
                signed in as <code>you@example.com</code> ·{" "}
                <button className="link-button" type="button">Sign out</button>
              </p>
            </div>
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
      {inspectMode && hoverBox && (
        <div
          aria-hidden="true"
          style={{
            position: "fixed",
            top: hoverBox.top,
            left: hoverBox.left,
            width: hoverBox.width,
            height: hoverBox.height,
            border: "1px solid var(--cyan)",
            boxShadow: "0 0 0 1px rgba(103,232,249,0.24)",
            pointerEvents: "none",
            zIndex: 1000,
          }}
        >
          <span
            style={{
              position: "absolute",
              left: 0,
              top: -22,
              maxWidth: 240,
              overflow: "hidden",
              textOverflow: "ellipsis",
              whiteSpace: "nowrap",
              borderRadius: "var(--radius-sm)",
              background: "var(--cyan)",
              color: "#082f36",
              fontSize: "var(--text-xs)",
              padding: "2px 6px",
            }}
          >
            {hoverBox.label}
          </span>
        </div>
      )}
      {selectedBox && (
        <div
          aria-hidden="true"
          style={{
            position: "fixed",
            top: selectedBox.top,
            left: selectedBox.left,
            width: selectedBox.width,
            height: selectedBox.height,
            border: "2px solid var(--status-online)",
            pointerEvents: "none",
            zIndex: 999,
          }}
        />
      )}
    </div>
  );
}
