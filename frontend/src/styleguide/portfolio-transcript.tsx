import { useState, type ReactNode } from "react";
import {
  ActivityIcon,
  ArrowDownIcon,
  CheckCircle2Icon,
  ChevronDownIcon,
  CopyIcon,
  FlaskConicalIcon,
  ImageIcon,
  LinkIcon,
  MessageSquareIcon,
  SquareTerminalIcon,
  TimerIcon,
  XIcon,
} from "lucide-react";
import { ChatComposer, type RunComposerMode } from "../ChatComposer";
import { McpIcon } from "../McpIcon";
import { WorkspaceShell } from "../WorkspaceShell";
import { AgentAvatarIcon, getSessionAvatar } from "../sessionAvatars";
import {
  BackLink,
  captionStyle,
  pageTitleStyle,
  sectionStyle,
  styleguideShellStyle,
} from "./shared";

type HighlightTarget = "assistant" | "user" | "activity";
type ComposerSpecimenState = "ready" | "streaming" | "readonly";
type ActiveSurface = "transcript" | "composer" | "both";

const HIGHLIGHT_TARGETS: { id: HighlightTarget; label: string }[] = [
  { id: "assistant", label: "assistant link target" },
  { id: "user", label: "quoted user turn" },
  { id: "activity", label: "active turn child" },
];

const COMPOSER_STATES: { id: ComposerSpecimenState; label: string }[] = [
  { id: "ready", label: "ready with text" },
  { id: "streaming", label: "streaming stop" },
  { id: "readonly", label: "read only" },
];

const ACTIVE_SURFACES: { id: ActiveSurface; label: string }[] = [
  { id: "transcript", label: "transcript selected" },
  { id: "composer", label: "input selected" },
  { id: "both", label: "both selected" },
];

function TranscriptMessage({
  variant,
  highlighted,
  children,
}: {
  variant: "assistant" | "user" | "system";
  highlighted?: boolean;
  children: ReactNode;
}) {
  const avatar = getSessionAvatar("portfolio-transcript-state");
  return (
    <div
      className="run-transcript-message"
      data-slot="message"
      data-variant={variant}
      data-role={variant}
      data-highlight={highlighted ? "true" : undefined}
      data-design-component="TranscriptMessage"
      data-design-state={highlighted ? "highlighted" : variant}
      data-inspectable
    >
      {variant === "assistant" && (
        <span className="run-msg-ai-avatar" aria-hidden="true">
          <AgentAvatarIcon avatar={avatar} className="run-msg-ai-icon" />
        </span>
      )}
      {variant === "user" && (
        <span className="run-msg-avatar" aria-hidden="true">
          <span className="avatar">NG</span>
        </span>
      )}
      {variant === "system" && (
        <span className="run-msg-system-avatar" aria-hidden="true">
          <CheckCircle2Icon size={16} strokeWidth={2.1} />
        </span>
      )}
      <div className="run-transcript-message-content" data-slot="message-content">
        <div className="run-transcript-message-text" data-slot="message-text">
          {children}
        </div>
        <div className="run-msg-footer" data-always-visible>
          {variant !== "system" && (
            <>
              <button className="run-msg-action run-msg-copy" type="button" aria-label="Copy message">
                <CopyIcon size={12} aria-hidden="true" />
              </button>
              <button className="run-msg-action run-msg-link" type="button" aria-label="Copy message link">
                <LinkIcon size={12} aria-hidden="true" />
              </button>
            </>
          )}
          <div className="run-msg-timings">
            <span className="run-msg-timing-row">
              <TimerIcon size={9} aria-hidden="true" />
              1.4s
            </span>
          </div>
        </div>
      </div>
    </div>
  );
}

function RunningTool({ highlighted }: { highlighted?: boolean }) {
  return (
    <div
      className="run-transcript-tool"
      data-state="running"
      data-highlight={highlighted ? "true" : undefined}
      data-design-component="TranscriptRunningTool"
      data-design-state={highlighted ? "highlighted" : "running"}
      data-inspectable
    >
      <span className="run-transcript-tool-connector" aria-hidden="true">
        <span className="run-transcript-tool-dot" />
      </span>
      <div className="run-transcript-tool-content">
        <button className="run-transcript-tool-header" type="button" aria-expanded={true}>
          <span className="run-transcript-tool-icon tool-color-bash">
            <SquareTerminalIcon size={14} strokeWidth={2} aria-hidden="true" />
          </span>
          <span className="run-transcript-tool-label">bash: inspect transcript selectors</span>
          <span className="run-tool-timing">
            <span>19:04:18</span>
            <span className="run-tool-timing-arrow">to</span>
            <span className="run-tool-timing-running">
              <TimerIcon className="run-tool-timing-spinner run-spin" size={12} aria-hidden="true" />
            </span>
          </span>
          <span className="run-transcript-tool-chevron">
            <ChevronDownIcon size={14} className="run-chevron-icon" aria-hidden="true" />
          </span>
        </button>
        <div className="run-transcript-tool-body">
          <div className="run-transcript-tool-section-title">command</div>
          <pre className="run-tool-bash-cmd">rg "data-highlight|data-active" frontend/src</pre>
          <div className="run-transcript-tool-section-title">output</div>
          <pre className="run-tool-bash-out">frontend/src/App.tsx: data-highlight on deep-linked bubbles</pre>
        </div>
      </div>
    </div>
  );
}

function ActiveTurnActivity({
  active,
  highlighted,
}: {
  active: boolean;
  highlighted?: boolean;
}) {
  return (
    <div
      className="run-turn-activity"
      data-state="open"
      data-active={active ? "true" : undefined}
      data-design-component="TranscriptTurnActivity"
      data-design-state={active ? "active" : "rest"}
      data-inspectable
    >
      <button type="button" className="run-turn-activity-header" aria-expanded={true}>
        <span className="run-turn-activity-icon" aria-hidden="true">
          <ActivityIcon size={14} strokeWidth={2} />
        </span>
        <span className="run-turn-activity-label">Turn activity</span>
        <span className="run-turn-activity-summary">
          1 running shell / 1 edit candidate / 2 progress notes
        </span>
        <span className="run-tool-timing">
          <span>19:04:14</span>
          <span className="run-tool-timing-arrow">to</span>
          <span className="run-tool-timing-running">
            <TimerIcon className="run-tool-timing-spinner run-spin" size={12} aria-hidden="true" />
          </span>
        </span>
        <span className="run-turn-activity-chevron">
          <ChevronDownIcon size={14} className="run-chevron-icon" aria-hidden="true" />
        </span>
      </button>
      <div className="run-turn-activity-body">
        <RunningTool highlighted={highlighted} />
        <TranscriptMessage variant="assistant" highlighted={highlighted}>
          <p style={{ margin: 0 }}>
            I found the highlight hook and the active turn data attribute. The portfolio keeps both states
            visible without needing a live session ledger.
          </p>
        </TranscriptMessage>
      </div>
    </div>
  );
}

function TranscriptSpecimen({
  highlightTarget,
}: {
  highlightTarget: HighlightTarget;
}) {
  return (
    <div
      className="run-transcript run-transcript-claude"
      data-design-component="TranscriptPane"
      data-design-state={`highlight-${highlightTarget}`}
      data-inspectable
    >
      <TranscriptMessage variant="system">
        <p style={{ margin: 0 }}>Design portfolio fixture loaded with static transcript state.</p>
      </TranscriptMessage>
      <TranscriptMessage variant="user" highlighted={highlightTarget === "user"}>
        <p style={{ margin: 0 }}>
          Show me the active transcript turn and the text box without waiting for real session data.
        </p>
      </TranscriptMessage>
      <ActiveTurnActivity
        active={highlightTarget === "activity"}
        highlighted={highlightTarget === "activity"}
      />
      <TranscriptMessage variant="assistant" highlighted={highlightTarget === "assistant"}>
        <p style={{ margin: 0 }}>
          The deep-link pulse is applied with <code className="run-markdown-inline-code">data-highlight</code>
          on the message content. The footer stays visible while the pulse is active.
        </p>
      </TranscriptMessage>
    </div>
  );
}

function ComposerToolButtons() {
  return (
    <>
      <button className="run-composer-icon-btn" type="button" aria-label="Attach image">
        <ImageIcon className="run-composer-icon" aria-hidden="true" />
      </button>
      <span className="run-usage-ring" data-level="mid" aria-label="64 percent context used">
        <svg className="run-usage-ring-svg" viewBox="0 0 36 36" aria-hidden="true">
          <circle cx="18" cy="18" r="15" fill="none" stroke="currentColor" strokeOpacity="0.18" strokeWidth="3" />
          <circle
            cx="18"
            cy="18"
            r="15"
            fill="none"
            stroke="currentColor"
            strokeDasharray="64 100"
            strokeLinecap="round"
            strokeWidth="3"
            transform="rotate(-90 18 18)"
          />
        </svg>
        <span className="run-usage-ring-text">64</span>
      </span>
      <button
        className="run-composer-icon-btn run-composer-action-btn run-test-action-btn is-ready"
        type="button"
        aria-label="Open test environment"
      >
        <FlaskConicalIcon className="run-composer-icon" aria-hidden="true" />
      </button>
      <button className="run-composer-icon-btn run-command-menu-btn" type="button" aria-label="Open slash commands">
        <MessageSquareIcon className="run-composer-icon" aria-hidden="true" />
      </button>
      <button className="run-composer-icon-btn run-command-menu-btn" type="button" aria-label="Open MCP menu">
        <McpIcon className="run-composer-icon" aria-hidden="true" />
      </button>
    </>
  );
}

function ComposerSpecimen({
  state,
  active,
}: {
  state: ComposerSpecimenState;
  active: boolean;
}) {
  const [permissionMode, setPermissionMode] = useState<RunComposerMode>("default");
  const readonly = state === "readonly";
  return (
    <ChatComposer
      key={state}
      className={[
        "run-composer-runpane",
        "run-composer-interactive",
        active ? "styleguide-surface-active styleguide-composer-surface-active" : "",
        readonly ? "run-composer-readonly" : "",
      ].filter(Boolean).join(" ")}
      placeholder="Ask for the next transcript design pass"
      initialText={
        readonly
          ? "This session is read only, but the composer remains in place for visual parity."
          : "Tune the highlighted transcript bubble and keep the active turn easy to scan."
      }
      onSubmit={() => {}}
      permissionMode={permissionMode}
      onPermissionModeChange={setPermissionMode}
      sendByCtrlEnter={false}
      hintSuffix=" / slash commands"
      disabled={readonly}
      canSubmit={!readonly}
      controlsDisabled={readonly}
      submitStatus={state === "streaming" ? "streaming" : undefined}
      onStop={() => {}}
      toolButtons={<ComposerToolButtons />}
    />
  );
}

function PortfolioTabs() {
  return (
    <>
      <button className="run-tab run-tab-active" type="button" aria-pressed={true}>
        Transcript
      </button>
      <button className="run-tab" type="button">
        Files
      </button>
      <button className="run-tab" type="button">
        Settings
      </button>
    </>
  );
}

function SegmentedControl<T extends string>({
  items,
  value,
  onChange,
}: {
  items: { id: T; label: string }[];
  value: T;
  onChange: (value: T) => void;
}) {
  return (
    <div className="run-settings-tabs">
      {items.map((item) => (
        <button
          key={item.id}
          className={`run-settings-tab${value === item.id ? " is-active" : ""}`}
          type="button"
          aria-pressed={value === item.id}
          onClick={() => onChange(item.id)}
        >
          {item.label}
        </button>
      ))}
    </div>
  );
}

export function StyleguidePortfolioTranscript() {
  const [highlightTarget, setHighlightTarget] = useState<HighlightTarget>("assistant");
  const [composerState, setComposerState] = useState<ComposerSpecimenState>("ready");
  const [activeSurface, setActiveSurface] = useState<ActiveSurface>("both");
  const transcriptActive = activeSurface === "transcript" || activeSurface === "both";
  const composerActive = activeSurface === "composer" || activeSurface === "both";

  return (
    <div style={styleguideShellStyle}>
      <div style={{ maxWidth: 1080 }}>
        <BackLink />
        <h1 style={pageTitleStyle}>portfolio scene: transcript states</h1>
        <p style={{ ...captionStyle, maxWidth: "74ch" }}>
          Static fixture for the transcript pane's deep-link highlight, selected surface state, active turn group, and shared input box.
        </p>

        <section style={{ ...sectionStyle, display: "grid", gap: 12 }}>
          <div style={{ display: "flex", flexWrap: "wrap", gap: 12, alignItems: "center" }}>
            <SegmentedControl
              items={ACTIVE_SURFACES}
              value={activeSurface}
              onChange={setActiveSurface}
            />
            <SegmentedControl
              items={HIGHLIGHT_TARGETS}
              value={highlightTarget}
              onChange={setHighlightTarget}
            />
            <SegmentedControl
              items={COMPOSER_STATES}
              value={composerState}
              onChange={setComposerState}
            />
          </div>

          <div
            style={{
              border: "1px solid var(--border-soft)",
              borderRadius: "var(--radius-md)",
              background: "var(--bg-base)",
              height: 720,
              overflowX: "auto",
              overflowY: "hidden",
            }}
          >
            <div style={{ height: "100%", minWidth: 760 }}>
              <WorkspaceShell
                className="styleguide-transcript-focus-shell"
                title={
                  <>
                    <button className="run-header-name-btn" type="button">
                      transcript-states
                    </button>
                    <p className="run-header-sub">mocked codex gui session</p>
                  </>
                }
                tabs={<PortfolioTabs />}
                body={<TranscriptSpecimen highlightTarget={highlightTarget} />}
                bodyClassName={transcriptActive ? "styleguide-surface-active styleguide-transcript-surface-active" : undefined}
                bodyAriaLabel="Transcript"
                floatingBetweenBodyAndComposer={
                  <button
                    className="run-scroll-to-bottom run-scroll-to-bottom-pending"
                    type="button"
                    aria-label="Scroll to latest transcript entry"
                  >
                    <ArrowDownIcon size={15} aria-hidden="true" />
                    <span className="run-scroll-to-bottom-count">2 new</span>
                  </button>
                }
                composerAbove={
                  <div className="run-queued-followups" data-design-component="QueuedFollowups" data-inspectable>
                    <div className="run-queued-followups-head">
                      <span>queued follow-up</span>
                      <span>1 item</span>
                    </div>
                    <div className="run-queued-followups-list">
                      <div className="run-queued-followup">
                        <span className="run-queued-followup-index">1</span>
                        <span className="run-queued-followup-text">
                          Compare the pulse radius against the active turn row and the composer focus ring.
                        </span>
                        <button className="run-queued-followup-action" type="button" aria-label="Send queued follow-up">
                          <ArrowDownIcon size={14} aria-hidden="true" />
                        </button>
                        <button className="run-queued-followup-action" type="button" aria-label="Remove queued follow-up">
                          <XIcon size={14} aria-hidden="true" />
                        </button>
                      </div>
                    </div>
                  </div>
                }
                composer={<ComposerSpecimen state={composerState} active={composerActive} />}
              />
            </div>
          </div>
        </section>
      </div>
    </div>
  );
}
