// Catalog landing page for /_styleguide. Lists every per-section route
// under styleguide/ as a categorized card grid; the SELECT ELEMENT
// inspector wraps the whole page so reviewers can pick any element here
// and have it posted to /api/design/selection.

import { StyleguideInspector } from "./inspector";
import {
  captionStyle,
  headStyle,
  MainAppButton,
  pageTitleStyle,
  sectionStyle,
  styleguideTopNavStyle,
} from "./shared";

const CATEGORIES: { name: string; entries: { slug: string; label: string; sub: string }[] }[] = [
  {
    name: "foundations",
    entries: [
      { slug: "colors", label: "colors", sub: "surface + semantic swatches" },
      { slug: "type", label: "type", sub: "Archivo scale" },
      { slug: "spacing", label: "spacing & radii", sub: "ladder" },
    ],
  },
  {
    name: "elements",
    entries: [
      { slug: "buttons", label: "buttons", sub: "primary, secondary, link" },
      { slug: "status-dot", label: "status dot", sub: "lifecycle + agent states" },
      { slug: "mode-chip", label: "mode chip", sub: "provider + GUI/CLI chips" },
      { slug: "tool-icons", label: "tool icons", sub: "current tool families" },
      { slug: "mcp-icon", label: "mcp icon", sub: "MCP servers + tool calls" },
    ],
  },
  {
    name: "components",
    entries: [
      { slug: "new-session-row", label: "session launcher", sub: "home setup + composer" },
      { slug: "run-header-tabs", label: "run header tabs", sub: "side-pane nav" },
      { slug: "session-row", label: "session row", sub: "sidebar list entry" },
      { slug: "mode-dropdown", label: "runtime controls", sub: "provider + interaction" },
      { slug: "welcome-card", label: "welcome card", sub: "boot/onboarding shell" },
      { slug: "error-pill", label: "error pill", sub: "inline transient errors" },
      { slug: "boot-state", label: "boot state", sub: "full-screen lifecycle" },
    ],
  },
  {
    name: "scenes",
    entries: [
      { slug: "portfolio-workspace", label: "portfolio: session workspace", sub: "full shell density" },
      { slug: "portfolio-onboarding", label: "portfolio: onboarding", sub: "first-run wall" },
      { slug: "portfolio-transcript", label: "portfolio: transcript states", sub: "highlight / active / composer" },
      { slug: "collapsed-turn-activity", label: "collapsed turn activity", sub: "divider chevron" },
      { slug: "question-heading", label: "ask-user-question heading", sub: "system-user question message" },
      { slug: "prompt-collapse-parity", label: "prompt collapse parity", sub: "one-line expand/collapse footer" },
      { slug: "mobile-shell", label: "compact shell", sub: "phone top bar + drawer + gate" },
    ],
  },
  {
    name: "features",
    entries: [
      { slug: "avatars", label: "agent avatar pool", sub: "picker + circle + 2 squares" },
    ],
  },
];

export function StyleguideIndex() {
  return (
    <StyleguideInspector>
      <div style={{ maxWidth: 880 }}>
        <div style={styleguideTopNavStyle}>
          <h1 style={pageTitleStyle}>tank-operator — styleguide</h1>
          <MainAppButton />
        </div>
        <p style={{ ...captionStyle, marginBottom: 24 }}>
          Visual catalog of components shipped by tank-operator's React app.
          Reviewers get this URL alongside every PR; agents update it whenever
          a component changes. See <code>docs/styleguide-contract.md</code> in
          the glimmung repo for the contract.
        </p>

        {CATEGORIES.map((category) => (
          <section key={category.name} style={sectionStyle}>
            <h2 style={headStyle}>{category.name}</h2>
            <ul
              style={{
                listStyle: "none",
                padding: 0,
                margin: 0,
                display: "grid",
                gridTemplateColumns: "repeat(auto-fill, minmax(220px, 1fr))",
                gap: 8,
              }}
            >
              {category.entries.map((entry) => (
                <li key={entry.slug}>
                  <a
                    href={`/_styleguide/${entry.slug}`}
                    style={{
                      display: "block",
                      padding: "12px 14px",
                      border: "1px solid var(--border-soft)",
                      borderRadius: "var(--radius-md)",
                      color: "var(--text-body)",
                      textDecoration: "none",
                      background: "var(--bg-elevated)",
                    }}
                  >
                    <span
                      style={{
                        display: "block",
                        fontSize: "var(--text-sm)",
                        color: "var(--accent-fg)",
                      }}
                    >
                      {entry.label} →
                    </span>
                    <span
                      style={{
                        display: "block",
                        fontSize: "var(--text-xs)",
                        color: "var(--text-faint)",
                        marginTop: 2,
                      }}
                    >
                      {entry.sub}
                    </span>
                  </a>
                </li>
              ))}
            </ul>
          </section>
        ))}
      </div>
    </StyleguideInspector>
  );
}
