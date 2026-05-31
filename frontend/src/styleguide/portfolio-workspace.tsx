// One section per route — pulls a copy of the original section's JSX
// out of the monolithic StyleguideView so feature pages can iterate
// independently. Keep behavior + markup identical to what was inline
// before; this is a pure structural move.

import {
  ActivityIcon,
  FolderIcon,
  InfoIcon,
  MonitorIcon,
  SettingsIcon,
  XIcon,
} from "lucide-react";
import { ProviderIcon } from "../providerIcons";
import { AgentAvatarIcon, requireSessionAvatar } from "../sessionAvatars";
import {
  BackLink,
  captionStyle,
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
frontend/src/styleguide/run-header-tabs.tsx

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
        <div className="sidebar-list">
          <div className="sidebar-list-head">
            <div className="sidebar-section-label">Sessions</div>
            <button className="sidebar-new-session" type="button" aria-label="New session" title="new session">
              <span className="row-icon">+</span>
            </button>
          </div>
          <ul className="sessions">
            <li className="is-open">
              <AgentAvatarIcon avatar={requireSessionAvatar("jp1-raptor")} className="session-avatar" />
              <div className="session-row-top">
                <span className="session-open">
                  <span className="session-id">design-showcase</span>
                </span>
                <button className="session-delete" aria-label="delete session" type="button">
                  <XIcon size={14} aria-hidden="true" />
                </button>
              </div>
              <div className="session-row-bottom">
                <span className="status-dot status-agent-working" aria-label="status: Agent working" />
                <span className="mode mode-icon-only mode-provider-chip" title="Codex GUI" aria-label="Codex GUI">
                  <ProviderIcon provider="codex" className="mode-provider-icon" />
                  <span className="sr-only">codex-gui</span>
                </span>
                <span className="mode mode-icon-only mode-interaction-chip" title="gui" aria-label="gui">
                  <MonitorIcon className="mode-interaction-icon" aria-hidden="true" />
                </span>
                <span className="session-stats">
                  <span className="session-stat" title="ready 28s after request">
                    <span aria-hidden="true">↓</span>
                    <span>28s</span>
                  </span>
                  <span className="session-stat" title="running 7m">
                    <span aria-hidden="true">↑</span>
                    <span>7m</span>
                  </span>
                </span>
              </div>
            </li>
            <li>
              <AgentAvatarIcon avatar={requireSessionAvatar("jp1-sattler")} className="session-avatar" />
              <div className="session-row-top">
                <span className="session-open">
                  <span className="session-id">avatar-review</span>
                </span>
                <button className="session-delete" aria-label="delete session" type="button">
                  <XIcon size={14} aria-hidden="true" />
                </button>
              </div>
              <div className="session-row-bottom">
                <span className="status-dot status-agent-needs-input" aria-label="status: Needs input" />
                <span className="mode mode-icon-only mode-provider-chip" title="Claude GUI" aria-label="Claude GUI">
                  <ProviderIcon provider="anthropic" className="mode-provider-icon" />
                  <span className="sr-only">claude-gui</span>
                </span>
                <span className="mode mode-icon-only mode-interaction-chip" title="gui" aria-label="gui">
                  <MonitorIcon className="mode-interaction-icon" aria-hidden="true" />
                </span>
              </div>
            </li>
          </ul>
        </div>
        <div className="sidebar-footer">
          <button className="profile" type="button" title="nelson@example.com">
            <span className="avatar" aria-hidden="true">NO</span>
            <span className="profile-text">
              <span className="profile-name">nelson@example.com</span>
            </span>
          </button>
        </div>
      </aside>
      <section className="run-panel">
        <header className="run-header">
          <div className="run-header-title">
            <button className="run-header-name-btn" type="button">design-showcase</button>
          </div>
          <nav className="run-tabs" aria-label="Session actions">
            <button className="run-tab run-turns-trigger" type="button" aria-pressed={false} disabled title="Turns are available once the agent has turn activity">
              <ActivityIcon className="run-tab-icon" aria-hidden="true" />
              <span>Turns</span>
            </button>
            <button className="run-tab run-shell-tasks-trigger" type="button" aria-pressed={false} title="Background">
              <ActivityIcon className="run-tab-icon" aria-hidden="true" />
              <span>Background</span>
              <span className="run-shell-tasks-count" data-active="true" aria-label="2 background items">2</span>
            </button>
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
