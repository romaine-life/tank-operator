// One section per route — pulls a copy of the original section's JSX
// out of the monolithic StyleguideView so feature pages can iterate
// independently. Keep behavior + markup identical to what was inline
// before; this is a pure structural move.

import { FolderIcon, InfoIcon, SettingsIcon } from "lucide-react";
import { ProviderIcon } from "../providerIcons";
import {
  BackLink,
  captionStyle,
  IconChevronDown,
  IconKey,
  IconWrench,
  pageTitleStyle,
  portfolioFrameStyle,
  sectionStyle,
  styleguideShellStyle,
} from "./shared";

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
                <span className="session-open">
                  <span className="session-id">design-showcase</span>
                </span>
                <button className="session-delete" aria-label="delete session" type="button">×</button>
              </div>
              <div className="session-row-bottom">
                <span className="status-dot status-active" aria-label="status active" />
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
                <span className="session-open">
                  <span className="session-id">avatar-review</span>
                </span>
                <button className="session-delete" aria-label="delete session" type="button">×</button>
              </div>
              <div className="session-row-bottom">
                <span className="status-dot status-pending" aria-label="status pending" />
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

export function StyleguidePortfolioWorkspace() {
  return (
    <div style={styleguideShellStyle}>
      <div style={{ maxWidth: 880 }}>
        <BackLink />
        <h1 style={pageTitleStyle}>portfolio scene: session workspace</h1>
        <p style={captionStyle}>
          Full shell composition for reviewing density, sidebar hierarchy,
          run header tabs, and terminal contrast together.
        </p>
        <section style={sectionStyle}>
          <div style={portfolioFrameStyle}>
            <PortfolioWorkspaceScene />
          </div>
        </section>
      </div>
    </div>
  );
}
