// Feature-level styleguide page for the agent avatar pool. Mounted by
// main.tsx at /_styleguide/avatars. Carves out its own scrollable page so
// the picker + live render contexts stay anchored together while you
// iterate per slug — the index-level styleguide is a vertical catalog
// and was getting cramped for stateful previews like this one.
//
// Contract (see also docs/styleguide-contract.md in the glimmung repo):
// when you change AGENT_AVATARS, the .session-avatar render contract,
// or how avatars get assigned to sessions, update this page in the same
// PR. The avatar pool's other render contexts (.run-msg-ai-icon,
// .run-status-avatar) live here too because the picker drives all three
// at once.

import { useState } from "react";
import { ProviderIcon } from "../providerIcons";
import { AGENT_AVATARS, AgentAvatarIcon } from "../sessionAvatars";
import {
  BackLink,
  captionStyle,
  headStyle,
  pageTitleStyle,
  sectionStyle,
  styleguideShellStyle,
} from "./shared";

export function StyleguideAvatars() {
  const [selectedAvatarId, setSelectedAvatarId] = useState<string>(
    AGENT_AVATARS[0]?.id ?? "",
  );
  const selectedAvatar =
    AGENT_AVATARS.find((a) => a.id === selectedAvatarId) ?? AGENT_AVATARS[0];

  return (
    <div style={styleguideShellStyle}>
      <div style={{ maxWidth: 880 }}>
        <BackLink />
        <h1 style={pageTitleStyle}>agent avatar pool</h1>
        <p style={{ ...captionStyle, marginBottom: 24 }}>
          {AGENT_AVATARS.length} entries in <code>AGENT_AVATARS</code>;
          assigned to a session by a stable hash of <code>session_id</code>{" "}
          (<code>getSessionAvatar</code>). Sources live under{" "}
          <code>frontend/public/assets/avatars/jp1-*.png</code>. Three
          render contexts ship: <code>.session-avatar</code> in the sidebar
          (42px, circle-cropped, translucent backdrop),{" "}
          <code>.run-msg-ai-icon</code> on transcript messages (~22px
          square), <code>.run-status-avatar</code> in the run-status pill
          (~18px square). The picker swaps all three live so the
          circle-crop on the sidebar surface — the only one that eats
          corners — can be vetted per slug.
        </p>

        {selectedAvatar && (
          <section style={sectionStyle}>
            <h2 style={headStyle}>pick a slug</h2>
            <div
              style={{
                display: "grid",
                gridTemplateColumns: "repeat(auto-fill, minmax(64px, 80px))",
                gap: 10,
                margin: "12px 0 24px",
              }}
              role="radiogroup"
              aria-label="avatar picker"
            >
              {AGENT_AVATARS.map((avatar) => {
                const isSelected = avatar.id === selectedAvatar.id;
                return (
                  <button
                    key={avatar.id}
                    type="button"
                    role="radio"
                    aria-checked={isSelected}
                    onClick={() => setSelectedAvatarId(avatar.id)}
                    title={`${avatar.name} (${avatar.id})`}
                    data-design-component="AvatarPickerSwatch"
                    data-design-state={isSelected ? "selected" : "rest"}
                    style={{
                      width: 80,
                      height: 80,
                      padding: 0,
                      borderRadius: "var(--radius-sm)",
                      border: isSelected
                        ? "2px solid var(--accent-fg)"
                        : "1px solid var(--border-soft)",
                      background: "transparent",
                      cursor: "pointer",
                      overflow: "hidden",
                      outline: "none",
                    }}
                  >
                    <img
                      src={avatar.src}
                      alt={avatar.name}
                      style={{
                        width: "100%",
                        height: "100%",
                        objectFit: "cover",
                        display: "block",
                      }}
                    />
                  </button>
                );
              })}
            </div>

            <h2 style={headStyle}>live render</h2>
            <p style={captionStyle}>
              All three contexts are driven by the picker above. Hover the
              sidebar tile to see the circle mask + the translucent
              backdrop — that's the surface that actually decides whether
              a source crop survives.
            </p>
            <div
              style={{
                display: "flex",
                flexWrap: "wrap",
                gap: 28,
                alignItems: "flex-start",
              }}
            >
              {/* sidebar — circle-cropped */}
              <div style={{ flex: "1 1 320px", minWidth: 280 }}>
                <div
                  style={{
                    fontSize: "var(--text-xs)",
                    color: "var(--text-muted)",
                    marginBottom: 6,
                  }}
                >
                  sidebar · <code>.session-avatar</code> · 42px · circle
                </div>
                <ul
                  className="sessions"
                  style={{
                    listStyle: "none",
                    padding: 0,
                    margin: 0,
                    maxWidth: 320,
                  }}
                >
                  <li>
                    <AgentAvatarIcon
                      avatar={selectedAvatar}
                      className="session-avatar"
                    />
                    <div className="session-row-top">
                      <span
                        className="status-dot status-active"
                        aria-label="status active"
                      />
                      <button className="session-open" type="button">
                        <span className="session-id">{selectedAvatar.name}</span>
                      </button>
                    </div>
                    <div className="session-row-bottom">
                      <span
                        className="mode mode-claude_cli mode-icon-only"
                        title="Claude CLI"
                        aria-label="Claude CLI"
                      >
                        <ProviderIcon
                          provider="anthropic"
                          className="mode-provider-icon"
                        />
                        <span className="sr-only">claude-cli</span>
                      </span>
                    </div>
                  </li>
                </ul>
              </div>

              {/* transcript message header — square */}
              <div style={{ flex: "0 0 auto" }}>
                <div
                  style={{
                    fontSize: "var(--text-xs)",
                    color: "var(--text-muted)",
                    marginBottom: 6,
                  }}
                >
                  transcript · <code>.run-msg-ai-icon</code> · ~22px · square
                </div>
                <div
                  style={{
                    display: "inline-flex",
                    alignItems: "center",
                    gap: 8,
                    padding: "10px 14px",
                    background: "var(--bg-elevated)",
                    border: "1px solid var(--border-soft)",
                    borderRadius: "var(--radius-md)",
                  }}
                >
                  <AgentAvatarIcon
                    avatar={selectedAvatar}
                    className="run-msg-ai-icon"
                  />
                  <span
                    style={{
                      color: "var(--text-faint)",
                      fontSize: "var(--text-sm)",
                    }}
                  >
                    Done — Edit · 4 changes
                  </span>
                </div>
              </div>

              {/* run-status pill — square */}
              <div style={{ flex: "0 0 auto" }}>
                <div
                  style={{
                    fontSize: "var(--text-xs)",
                    color: "var(--text-muted)",
                    marginBottom: 6,
                  }}
                >
                  status pill · <code>.run-status-avatar</code> · ~18px · square
                </div>
                <div
                  className="run-status-bar run-status-bar-idle"
                  role="status"
                  style={{ display: "inline-flex", alignItems: "center" }}
                >
                  <span className="run-status-icon">
                    <AgentAvatarIcon
                      avatar={selectedAvatar}
                      className="run-status-avatar"
                    />
                  </span>
                  <span className="run-status-text">
                    <span className="run-status-verb">Done</span>
                  </span>
                </div>
              </div>
            </div>
          </section>
        )}
      </div>
    </div>
  );
}
