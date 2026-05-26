// Feature-level styleguide page for the agent avatar pool. Mounted by
// main.tsx at /_styleguide/avatars. Carves out its own scrollable page so
// the picker + live render contexts stay anchored together while you
// iterate per slug — the index-level styleguide is a vertical catalog
// and was getting cramped for stateful previews like this one.
//
// Contract (see also docs/styleguide-contract.md in the glimmung repo):
// when you change AGENT_AVATARS, the .session-avatar render contract,
// or how avatars get assigned to sessions, update this page in the same
// PR. The avatar pool's other render context (.run-msg-ai-icon) lives
// here too because the picker drives both at once.

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

  // Transient feedback on the "copy name" button. Bounces back to the
  // resting label after a beat so a second click can be distinguished
  // from the first; falls back to a manual-fail message when the
  // Clipboard API rejects (insecure context, denied permission).
  const [copyState, setCopyState] = useState<"idle" | "copied" | "failed">("idle");
  async function copySelectedName() {
    if (!selectedAvatar) return;
    try {
      await navigator.clipboard.writeText(selectedAvatar.name);
      setCopyState("copied");
    } catch {
      setCopyState("failed");
    }
    setTimeout(() => setCopyState("idle"), 1500);
  }

  return (
    <div style={styleguideShellStyle}>
      <div style={{ maxWidth: 880 }}>
        <BackLink />
        <h1 style={pageTitleStyle}>agent avatar pool</h1>
        <p style={{ ...captionStyle, marginBottom: 24 }}>
          {AGENT_AVATARS.length} entries in <code>AGENT_AVATARS</code>;
          resolved only from a durable assigned avatar id. Sources live under{" "}
          <code>frontend/public/assets/avatars/jp1-*.png</code>. Three
          render contexts ship: <code>.session-avatar</code> in the sidebar
          (42px, circle-cropped, edge-to-edge — no backdrop or padding) and{" "}
          <code>.run-msg-ai-icon</code> on transcript messages (also 42px,
          matching the sidebar so the avatar reads the same in both
          surfaces). Both are the source PNG clipped to a circle with no
          chrome, so the picker drives every surface at once.
        </p>

        {selectedAvatar && (
          <section style={sectionStyle}>
            <div
              style={{
                display: "flex",
                alignItems: "baseline",
                justifyContent: "space-between",
                gap: 16,
                flexWrap: "wrap",
                margin: "0 0 12px",
              }}
            >
              <h2 style={{ ...headStyle, margin: 0 }}>pick a slug</h2>
              <button
                type="button"
                onClick={copySelectedName}
                title={`Copy "${selectedAvatar.name}" to clipboard`}
                data-design-component="AvatarCopyNameButton"
                data-design-state={copyState}
                className="btn-secondary"
                style={{
                  fontSize: "var(--text-xs)",
                  padding: "6px 10px",
                  cursor: "pointer",
                }}
              >
                {copyState === "copied"
                  ? `copied "${selectedAvatar.name}"`
                  : copyState === "failed"
                    ? "copy failed (clipboard blocked)"
                    : `copy "${selectedAvatar.name}"`}
              </button>
            </div>
            <div
              style={{
                display: "grid",
                gridTemplateColumns: "repeat(auto-fill, minmax(64px, 80px))",
                gap: 10,
                margin: "0 0 24px",
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
                    onClick={() => {
                      setSelectedAvatarId(avatar.id);
                      // Stale "copied" feedback from a prior selection
                      // would otherwise read as "copied <new name>"
                      // even though only the prior name was actually
                      // written. Reset so the next copy click stands
                      // on its own.
                      setCopyState("idle");
                    }}
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
              All three contexts are driven by the picker above. The sidebar
              tile is a 42px circle mask with no backdrop — the source PNG
              itself is the visible shape, so a slug that doesn't fill a
              42px circle will read as a floating silhouette there.
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
                      <span className="session-open">
                        <span className="session-id">{selectedAvatar.name}</span>
                      </span>
                    </div>
                    <div className="session-row-bottom">
                      <span
                        className="status-dot status-active"
                        aria-label="status active"
                      />
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
                  transcript · <code>.run-msg-ai-icon</code> · 42px · circle
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

            </div>
          </section>
        )}
      </div>
    </div>
  );
}
