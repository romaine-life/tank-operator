// One section per route — pulls a copy of the original section's JSX
// out of the monolithic StyleguideView so feature pages can iterate
// independently. Keep behavior + markup identical to what was inline
// before; this is a pure structural move.

import { ProviderIcon } from "../providerIcons";
import { AgentAvatarIcon, getSessionAvatar } from "../sessionAvatars";
import {
  BackLink,
  captionStyle,
  pageTitleStyle,
  sectionStyle,
  styleguideShellStyle,
  TankIcon,
} from "./shared";

export function StyleguideSessionRow() {
  return (
    <div style={styleguideShellStyle}>
      <div style={{ maxWidth: 880 }}>
        <BackLink />
        <h1 style={pageTitleStyle}>session row</h1>
        <p style={captionStyle}>
          One row per session in the sidebar list. Top: status dot + session
          name + compact boot/runtime stats + delete affordance. Bottom: mode chip +
          optional inline actions (remote-control, rollout, save-credentials). Active
          row gets the <code>is-open</code> class; not styled here for brevity.
        </p>
        <section style={sectionStyle}>
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
      </div>
    </div>
  );
}
