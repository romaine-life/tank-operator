import { MonitorIcon, TerminalIcon, XIcon } from "lucide-react";
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
}: {
  provider: "anthropic" | "codex" | "hermes";
  interaction: "gui" | "cli";
  label: string;
}) {
  const Interaction = interaction === "gui" ? MonitorIcon : TerminalIcon;
  return (
    <>
      <span className="mode mode-icon-only mode-provider-chip" title={label} aria-label={label}>
        <ProviderIcon provider={provider} className="mode-provider-icon" />
        <span className="sr-only">{label}</span>
      </span>
      <span className="mode mode-icon-only mode-interaction-chip" title={interaction} aria-label={interaction}>
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
          inline actions such as save for config sessions, and repo chips for
          the repos a session worked on (create-time selection unioned with
          the repos its pod checked out at runtime).
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
                <ModePair provider="codex" interaction="gui" label="Codex GUI" />
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
              <div className="session-repos" aria-label="repos">
                <span className="session-repo-chip" title="nelsong6/tank-operator">tank-operator</span>
                <span className="session-repo-chip" title="nelsong6/glimmung">glimmung</span>
                <span className="session-repo-chip session-repo-more" title="nelsong6/infra-bootstrap, nelsong6/auth">
                  +2
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
      </div>
    </div>
  );
}
