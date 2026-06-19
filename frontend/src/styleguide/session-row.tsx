import { GitBranchIcon, MonitorIcon, TerminalIcon, XIcon } from "lucide-react";
import { ProviderIcon } from "../providerIcons";
import { AgentAvatarIcon, requireSessionAvatar } from "../sessionAvatars";
import {
  BackLink,
  captionStyle,
  pageTitleStyle,
  sectionStyle,
  styleguideShellStyle,
} from "./shared";

function ModePair({
  provider,
  interaction,
  label,
  restrictedGit = false,
}: {
  provider: "anthropic" | "codex";
  interaction: "gui" | "cli";
  label: string;
  restrictedGit?: boolean;
}) {
  // Mirrors App.tsx InteractionIcon: restricted Git is the default, so the chip
  // flags the *unrestricted* opt-out — an unrestricted GUI session swaps the
  // monitor glyph for a red git glyph and tints the chip.
  const showUnrestrictedGit = !restrictedGit && interaction === "gui";
  const Interaction = showUnrestrictedGit
    ? GitBranchIcon
    : interaction === "gui"
      ? MonitorIcon
      : TerminalIcon;
  const interactionLabel = showUnrestrictedGit ? "unrestricted git" : interaction;
  return (
    <>
      <span className="mode mode-icon-only mode-provider-chip" title={label} aria-label={label}>
        <ProviderIcon provider={provider} className="mode-provider-icon" />
        <span className="sr-only">{label}</span>
      </span>
      <span
        className={`mode mode-icon-only mode-interaction-chip${showUnrestrictedGit ? " is-unrestricted-git" : ""}`}
        title={interactionLabel}
        aria-label={interactionLabel}
      >
        <Interaction className="mode-interaction-icon" aria-hidden="true" />
      </span>
    </>
  );
}

export function StyleguideSessionRow() {
  return (
    <div style={styleguideShellStyle}>
      <div style={{ maxWidth: 880 }}>
        <BackLink />
        <h1 style={pageTitleStyle}>session row</h1>
        <p style={captionStyle}>
          Current sidebar rows: agent avatar, read-only name label, status dot,
          provider and interaction chips, boot/runtime stats, activity chips,
          and inline actions such as save for config sessions. Restricted
          (Tank-governed) Git is the default, so unrestricted sessions swap the
          GUI monitor glyph for a red git glyph as a standing reminder that the
          session has ungoverned Git access.
        </p>
        <p style={captionStyle}>
          A session spawned by another session (<code>spawn_run_session</code>)
          nests one tier under its origin: the tab keeps its right edge pinned,
          steps its left edge in, reads slightly smaller, and a ├─/└─ connector
          ties it to the parent above. Exactly one level — deeper lineage is
          clamped to the same tier.
        </p>
        <section style={sectionStyle}>
          <ul className="sessions" style={{ maxWidth: 420, listStyle: "none", padding: 0, margin: 0 }}>
            <li className="is-open is-skill-test">
              <AgentAvatarIcon avatar={requireSessionAvatar("jp1-raptor")} className="session-avatar" />
              <div className="session-row-top">
                <span className="session-open" title="design-showcase">
                  <span className="session-id">design-showcase</span>
                </span>
                <button className="session-delete" aria-label="delete session" type="button">
                  <XIcon size={14} aria-hidden="true" />
                </button>
              </div>
              <div className="session-row-bottom">
                <span className="status-dot status-agent-working" title="Agent working" aria-label="status: Agent working" />
                <ModePair provider="codex" interaction="gui" label="Codex GUI" restrictedGit />
                <span className="session-stats">
                  <span className="session-stat" title="ready 32s after request" aria-label="ready 32s after request">
                    <span aria-hidden="true">↓</span>
                    <span>32s</span>
                  </span>
                  <span className="session-stat" title="running 12m" aria-label="running 12m">
                    <span aria-hidden="true">↑</span>
                    <span>12m</span>
                  </span>
                </span>
              </div>
            </li>
            <li>
              <AgentAvatarIcon avatar={requireSessionAvatar("jp1-sattler")} className="session-avatar" />
              <div className="session-row-top">
                <span className="session-open" title="migration-plan">
                  <span className="session-id">migration-plan</span>
                </span>
                <button className="session-delete" aria-label="delete session" type="button">
                  <XIcon size={14} aria-hidden="true" />
                </button>
              </div>
              <div className="session-row-bottom">
                <span className="status-dot status-agent-needs-input" title="Needs input" aria-label="status: Needs input" />
                <ModePair provider="anthropic" interaction="gui" label="Claude GUI" restrictedGit />
              </div>
            </li>
            <li>
              <AgentAvatarIcon avatar={requireSessionAvatar("jp1-grant")} className="session-avatar" />
              <div className="session-row-top">
                <span className="session-open" title="unrestricted-git">
                  <span className="session-id">unrestricted-git</span>
                </span>
                <button className="session-delete" aria-label="delete session" type="button">
                  <XIcon size={14} aria-hidden="true" />
                </button>
              </div>
              <div className="session-row-bottom">
                <span className="status-dot status-agent-running" title="Agent working" aria-label="status: Agent working" />
                <ModePair provider="anthropic" interaction="gui" label="Claude GUI" />
              </div>
            </li>
            <li>
              <AgentAvatarIcon avatar={requireSessionAvatar("jp1-malcolm")} className="session-avatar" />
              <div className="session-row-top">
                <span className="session-open" title="codex-login">
                  <span className="session-id">codex-login</span>
                </span>
                <button className="session-delete" aria-label="delete session" type="button">
                  <XIcon size={14} aria-hidden="true" />
                </button>
              </div>
              <div className="session-row-bottom">
                <span className="status-dot status-active" title="Active" aria-label="status: Active" />
                <span className="mode mode-codex_config" title="Codex config" aria-label="Codex config">
                  codex-cfg
                </span>
                <button className="session-action" type="button" title="capture ~/.codex/auth.json from this pod and write it to KV">
                  save
                </button>
              </div>
            </li>
          </ul>
        </section>
        <section style={sectionStyle}>
          <p style={captionStyle}>
            Nested spawned sub-sessions: one origin with two children (the second
            is the last child, so it gets the └─ elbow). Mirrors the sidebar
            markup — each nested row carries <code>is-nested</code> and a
            <code> session-nest-connector</code>; the last carries{" "}
            <code>is-nested-last</code>. The guide is one continuous opaque spine:
            each row paints its height plus the single gap above it, so the
            segments butt-join without overshooting onto a tab or doubling the
            seam.
          </p>
          <ul className="sessions" style={{ maxWidth: 420, listStyle: "none", padding: 0, margin: 0 }}>
            <li data-depth={0}>
              <AgentAvatarIcon avatar={requireSessionAvatar("jp1-raptor")} className="session-avatar" />
              <div className="session-row-top">
                <span className="session-open" title="migration-plan">
                  <span className="session-id">migration-plan</span>
                </span>
                <button className="session-delete" aria-label="delete session" type="button">
                  <XIcon size={14} aria-hidden="true" />
                </button>
              </div>
              <div className="session-row-bottom">
                <span className="status-dot status-agent-working" title="Agent working" aria-label="status: Agent working" />
                <ModePair provider="anthropic" interaction="gui" label="Claude GUI" restrictedGit />
              </div>
            </li>
            <li className="is-nested" data-depth={1}>
              <span className="session-nest-connector" aria-hidden="true" />
              <AgentAvatarIcon avatar={requireSessionAvatar("jp1-sattler")} className="session-avatar" />
              <div className="session-row-top">
                <span className="session-open" title="repo-audit">
                  <span className="session-id">repo-audit</span>
                </span>
                <button className="session-delete" aria-label="delete session" type="button">
                  <XIcon size={14} aria-hidden="true" />
                </button>
              </div>
              <div className="session-row-bottom">
                <span className="status-dot status-agent-running" title="Agent working" aria-label="status: Agent working" />
                <ModePair provider="anthropic" interaction="gui" label="Claude GUI" restrictedGit />
              </div>
            </li>
            <li className="is-nested is-nested-last" data-depth={1}>
              <span className="session-nest-connector" aria-hidden="true" />
              <AgentAvatarIcon avatar={requireSessionAvatar("jp1-grant")} className="session-avatar" />
              <div className="session-row-top">
                <span className="session-open" title="flaky-test-hunt">
                  <span className="session-id">flaky-test-hunt</span>
                </span>
                <button className="session-delete" aria-label="delete session" type="button">
                  <XIcon size={14} aria-hidden="true" />
                </button>
              </div>
              <div className="session-row-bottom">
                <span className="status-dot status-agent-needs-input" title="Needs input" aria-label="status: Needs input" />
                <ModePair provider="codex" interaction="gui" label="Codex GUI" restrictedGit />
              </div>
            </li>
          </ul>
        </section>
      </div>
    </div>
  );
}
