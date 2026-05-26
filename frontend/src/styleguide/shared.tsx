// Module-scope helpers, types, constants, style objects, and tiny
// components that were previously in the monolithic StyleguideView.tsx.
// Each per-section route file under styleguide/ pulls what it needs from
// here so the visual catalog (the styleguide index + feature pages) stays
// consistent without duplicating swatch lists or styling tokens.

export const MODES = [
  "claude_cli",
  "claude_gui",
  "api_key",
  "config",
  "codex_cli",
  "codex_gui",
  "codex_exec_gui",
  "codex_app_server",
  "codex_config",
  "hermes_gui",
  "pi_cli",
  "pi_config",
] as const;
export const MODE_LABELS: Record<(typeof MODES)[number], string> = {
  claude_cli: "claude-cli",
  claude_gui: "claude-gui",
  api_key: "api",
  config: "config",
  codex_cli: "codex-cli",
  codex_gui: "codex-gui",
  codex_exec_gui: "codex-exec",
  codex_app_server: "codex-app",
  codex_config: "codex-cfg",
  hermes_gui: "hermes",
  pi_cli: "pi-cli",
  pi_config: "pi-cfg",
};
export const MODE_FULL_LABELS: Record<(typeof MODES)[number], string> = {
  claude_cli: "Claude CLI",
  claude_gui: "Claude GUI",
  api_key: "Claude API key",
  config: "Claude config",
  codex_cli: "Codex CLI",
  codex_gui: "Codex GUI",
  codex_exec_gui: "Codex Legacy",
  codex_app_server: "Codex App Server",
  codex_config: "Codex config",
  hermes_gui: "Hermes",
  pi_cli: "Pi CLI",
  pi_config: "Pi config",
};
export const MODE_ICONS: Partial<Record<(typeof MODES)[number], "anthropic" | "codex" | "hermes" | "pi">> = {
  claude_cli: "anthropic",
  claude_gui: "anthropic",
  codex_cli: "codex",
  codex_gui: "codex",
  codex_exec_gui: "codex",
  codex_app_server: "codex",
  hermes_gui: "hermes",
  pi_cli: "pi",
};
export const MODE_INTERACTIONS: Partial<Record<(typeof MODES)[number], "gui" | "cli">> = {
  claude_cli: "cli",
  claude_gui: "gui",
  codex_cli: "cli",
  codex_gui: "gui",
  codex_exec_gui: "gui",
  codex_app_server: "gui",
  hermes_gui: "gui",
  pi_cli: "cli",
};
export const STATUSES = [
  ["active", "Active"],
  ["pending", "Pending"],
  ["failed", "Failed"],
  ["agent-working", "Agent working"],
  ["agent-waiting", "Agent waiting"],
  ["agent-needs-input", "Needs input"],
  ["agent-stopping", "Stopping"],
  ["agent-error", "Agent error"],
] as const;
export const SURFACE_SWATCHES = [
  ["app", "--bg-app", "#171717"],
  ["sidebar", "--bg-sidebar", "rgba(13,13,13,0.88)"],
  ["control", "--bg-sidebar-control", "rgba(255,255,255,0.03)"],
  ["hover", "--bg-sidebar-hover", "rgba(255,255,255,0.075)"],
  ["active", "--bg-sidebar-active", "rgba(79,140,247,0.10)"],
] as const;
export const SEMANTIC_SWATCHES = [
  ["accent", "--accent-fg", "#b9d2fb"],
  ["remote", "--cyan", "#67e8f9"],
  ["online", "--status-online", "#34d399"],
  ["failed", "--status-error-fg", "#ef6f6f"],
  ["needs input", "--status-agent-needs-input", "#fb923c"],
] as const;
export const TYPE_SAMPLES = [
  ["xs", "--text-xs", "12px", "sidebar meta and compact labels"],
  ["sm", "--text-sm", "14px", "default chrome and body"],
  ["base", "--text-base", "16px", "brand and larger controls"],
  ["lg", "--text-lg", "18px", "run header session title"],
  ["2xl", "--text-2xl", "24px", "onboarding titles"],
] as const;
export const RADIUS_SAMPLES = [
  ["sm", "--radius-sm", "6px"],
  ["md", "--radius-md", "8px"],
  ["lg", "--radius-lg", "12px"],
  ["xl", "--radius-xl", "16px"],
  ["pill", "--radius-pill", "9999px"],
] as const;

export type InspectBox = {
  top: number;
  left: number;
  width: number;
  height: number;
  label: string;
};

export type DesignSelection = {
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

export function readableElementName(el: HTMLElement) {
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

export function cssHint(el: HTMLElement) {
  if (el.id) return `#${CSS.escape(el.id)}`;
  const testId = el.dataset.testid;
  if (testId) return `[data-testid="${testId}"]`;
  const component = el.dataset.designComponent;
  if (component) return `[data-design-component="${component}"]`;
  const className = Array.from(el.classList).slice(0, 2).map((name) => `.${CSS.escape(name)}`).join("");
  return `${el.tagName.toLowerCase()}${className}`;
}

export function nearestSpecimenHeading(el: HTMLElement, root: HTMLElement) {
  const section = el.closest("section");
  if (!section || !root.contains(section)) return null;
  return section.querySelector("h2")?.textContent?.replace(/\s+/g, " ").trim() || null;
}

export function inspectBoxFor(el: HTMLElement): InspectBox {
  const rect = el.getBoundingClientRect();
  return {
    top: rect.top,
    left: rect.left,
    width: rect.width,
    height: rect.height,
    label: el.dataset.designComponent || el.getAttribute("aria-label") || el.tagName.toLowerCase(),
  };
}

export function selectionForElement(el: HTMLElement, root: HTMLElement): DesignSelection {
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

export function IconWrench({ className }: { className?: string }) {
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

export function IconKey({ className }: { className?: string }) {
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

export function IconChevronDown({ className }: { className?: string }) {
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

export function TankIcon({ className }: { className?: string }) {
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

// Shared with feature-page components so the catalog index and the focused
// feature pages stay visually consistent.
export const sectionStyle: React.CSSProperties = {
  padding: "32px 0 24px",
  borderTop: "1px solid var(--border-subtle)",
};
export const headStyle: React.CSSProperties = {
  fontSize: "var(--text-xs)",
  textTransform: "uppercase",
  letterSpacing: "1.2px",
  color: "var(--text-muted)",
  margin: "0 0 4px 0",
};
export const captionStyle: React.CSSProperties = {
  fontSize: "var(--text-xs)",
  color: "var(--text-faint)",
  margin: "0 0 14px 0",
  maxWidth: "60ch",
};
export const rowStyle: React.CSSProperties = {
  display: "flex",
  flexWrap: "wrap",
  gap: "12px",
  alignItems: "center",
};
export const showcaseFrameStyle: React.CSSProperties = {
  border: "1px solid var(--border-soft)",
  borderRadius: "var(--radius-md)",
  background: "var(--bg-base)",
  overflow: "hidden",
};

// The styleguide chrome — used as the outer `<div style>` on the index and
// each feature page. height:100% + overflow:auto carves a scroll context
// out of the global `html, body, #root { overflow: hidden }` invariant
// that the main app relies on, so the styleguide is actually scrollable
// while the rest of the app keeps its locked viewport.
export const styleguideShellStyle: React.CSSProperties = {
  height: "100%",
  overflow: "auto",
  background: "var(--bg-app)",
  color: "var(--text-body)",
  fontFamily: "var(--font-primary)",
  padding: "32px 48px 64px",
};
export const portfolioFrameStyle: React.CSSProperties = {
  ...showcaseFrameStyle,
  height: 420,
  overflowX: "auto",
};

// Promoted from the previous StyleguideAvatars page-h1 style so every
// feature page uses the same h1 chrome.
export const pageTitleStyle: React.CSSProperties = {
  fontSize: "var(--text-xs)",
  fontWeight: 400,
  color: "var(--text-muted)",
  textTransform: "uppercase",
  letterSpacing: "1.4px",
  margin: "0 0 4px 0",
};

export function BackLink() {
  return (
    <p style={{ ...captionStyle, marginBottom: 4 }}>
      <a
        href="/_styleguide"
        style={{
          color: "var(--accent-fg)",
          textDecoration: "none",
        }}
      >
        ← tank-operator — styleguide
      </a>
    </p>
  );
}

export function Swatch({ label, token, value }: { label: string; token: string; value: string }) {
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
