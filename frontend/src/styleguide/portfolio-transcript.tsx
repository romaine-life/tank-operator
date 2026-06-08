import { useState, type ReactNode } from "react";
import {
  ActivityIcon,
  ArrowDownIcon,
  CheckCircle2Icon,
  ChevronDownIcon,
  ChevronUpIcon,
  CopyIcon,
  FlaskConicalIcon,
  ImageIcon,
  LinkIcon,
  MessageSquareIcon,
  MinusIcon,
  SquareTerminalIcon,
  TimerIcon,
  XIcon,
} from "lucide-react";
import { ChatComposer } from "../ChatComposer";
import { McpIcon } from "../McpIcon";
import { WorkspaceShell } from "../WorkspaceShell";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "../components/ui/select";
import { AgentAvatarIcon, getSessionAvatarByID } from "../sessionAvatars";
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
  ownedByActivity,
  showAssistantAvatar = !ownedByActivity,
  compact,
  children,
}: {
  variant: "assistant" | "user" | "system";
  highlighted?: boolean;
  ownedByActivity?: boolean;
  showAssistantAvatar?: boolean;
  compact?: boolean;
  children: ReactNode;
}) {
  const avatar = getSessionAvatarByID("jp1-malcolm");
  return (
    <div
      className="run-transcript-message"
      data-slot="message"
      data-variant={variant}
      data-role={variant}
      data-compact={compact ? "true" : undefined}
      data-owner={ownedByActivity ? "activity" : undefined}
      data-highlight={highlighted ? "true" : undefined}
      data-design-component="TranscriptMessage"
      data-design-state={highlighted ? "highlighted" : variant}
      data-inspectable
    >
      {variant === "assistant" && avatar && showAssistantAvatar && (
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
          {compact ? (
            <span
              className="run-msg-compact-text"
              title="Please inspect the completed turn with a long initiating prompt. The divider owns the section controls while the prompt preview stays readable."
            >
              Please inspect the completed turn with a long initiating prompt. The divider owns the section controls while the prompt preview stays readable.
            </span>
          ) : (
            children
          )}
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

function TurnThinkingMessage({
  active,
  highlighted,
}: {
  active: boolean;
  highlighted?: boolean;
}) {
  const avatar = getSessionAvatarByID("jp1-malcolm");
  return (
    <div
      className="run-transcript-message run-turn-thinking"
      data-slot="message"
      data-variant="assistant"
      data-role="assistant"
      data-kind="turn-thinking"
      data-active={active ? "true" : undefined}
      data-highlight={highlighted ? "true" : undefined}
      data-design-component="TranscriptTurnThinking"
      data-design-state={active ? "active" : "rest"}
      data-inspectable
    >
      {avatar && (
        <span className="run-msg-ai-avatar" aria-hidden="true">
          <AgentAvatarIcon avatar={avatar} className="run-msg-ai-icon" />
        </span>
      )}
      <button type="button" className="run-transcript-message-content run-turn-thinking-content">
        <span className="run-turn-thinking-lines">
          <span className="run-turn-thinking-label run-turn-thinking-shimmer">Thinking...</span>
          {/* Static specimen mirrors a mid-run turn so the styleguide shows the
              same metadata rows the live transcript renders. App.tsx drives the
              runtime from the local turn stopwatch and last activity from the
              projected activity shell; here we freeze representative values. */}
          <span className="run-turn-thinking-meta-row">
            <span className="run-turn-thinking-meta-label">Runtime</span>
            <span
              className="run-turn-thinking-duration"
              data-design-element="thinking-duration"
            >
              6m 12s
            </span>
          </span>
          <span className="run-turn-thinking-meta-row">
            <span className="run-turn-thinking-meta-label">Last activity</span>
            <span
              className="run-turn-thinking-last-activity"
              data-design-element="thinking-last-activity"
            >
              8s ago
            </span>
          </span>
        </span>
      </button>
    </div>
  );
}

function TurnViewSpecimen({ highlighted }: { highlighted?: boolean }) {
  return (
    <div className="run-turn-view" data-design-component="TurnView" data-inspectable>
      <div className="run-turn-titlebar-controls">
        <button
          type="button"
          className="run-turn-view-stats-toggle"
          aria-expanded={true}
          aria-label="Hide turn stats"
          title="Hide stats"
        >
          <span>Stats</span>
          <ChevronUpIcon size={14} aria-hidden="true" />
        </button>
        <Select value="turn-3" onValueChange={() => {}}>
          <SelectTrigger
            className="run-turn-view-select"
            size="sm"
            aria-label="Select turn"
          >
            <SelectValue />
          </SelectTrigger>
          <SelectContent
            className="run-turn-view-select-menu"
            position="popper"
            align="end"
          >
            <SelectItem value="turn-1" className="run-turn-view-select-item">
              Turn 1
            </SelectItem>
            <SelectItem value="turn-2" className="run-turn-view-select-item">
              Turn 2
            </SelectItem>
            <SelectItem value="turn-3" className="run-turn-view-select-item">
              Turn 3 (running)
            </SelectItem>
          </SelectContent>
        </Select>
      </div>
      <div
        className="run-turn-view-summary"
        data-design-component="TurnStatsPanel"
        data-design-state="expanded"
        data-inspectable
      >
        <span>complete</span>
        <span>1 shell / 1 edit candidate / 2 progress notes</span>
        <span title="9/1000 events on this page; 2375 total">
          9/1000 events
        </span>
        <span>2375 total</span>
        <span>19:04:14</span>
        <span>19:10:26</span>
      </div>
      <div
        className="run-turn-view-context"
        aria-label="Turn prompt"
        data-collapsed="false"
        data-context-loaded="true"
        data-design-component="TurnPromptContext"
        data-design-state="expanded-with-divider"
        data-inspectable
      >
        <div className="run-turn-view-context-head">
          <span className="run-turn-view-context-label">Prompt</span>
        </div>
        <TranscriptMessage variant="user" ownedByActivity>
          <p style={{ margin: 0 }}>
            Please inspect the completed turn with a long initiating prompt.
            The divider owns section collapse controls while the prompt label
            stays as plain section chrome.
          </p>
        </TranscriptMessage>
      </div>
      <div
        className="run-turn-view-context"
        aria-label="Turn prompt collapsed"
        data-collapsed="true"
        data-context-loaded="true"
        data-design-component="TurnPromptContext"
        data-design-state="collapsed-text-preview-controls-inline"
        data-inspectable
      >
        <div className="run-turn-view-context-head">
          <span className="run-turn-view-context-label">Prompt</span>
        </div>
        <TranscriptMessage variant="user" ownedByActivity compact>
          <p style={{ margin: 0 }}>
            Please inspect the completed turn with a long initiating prompt.
            The collapsed preview stays visible while section controls live on
            the divider.
          </p>
        </TranscriptMessage>
      </div>
      <div
        className="run-turn-view-context"
        aria-label="Turn prompt unavailable"
        data-collapsed="false"
        data-context-loaded="false"
        data-design-component="TurnPromptContext"
        data-design-state="context-unavailable-control-disabled"
        data-inspectable
      >
        <div className="run-turn-view-context-head">
          <span className="run-turn-view-context-label">Prompt</span>
        </div>
        <div className="run-turn-view-context-unavailable" role="status">
          Prompt context unavailable
        </div>
      </div>
      <div
        className="run-turn-activity-divider run-turn-view-activity-divider"
        data-design-component="TurnSectionDivider"
        data-design-state="prompt-and-activity-controls-present"
        data-inspectable
      >
        <div
          className="run-turn-activity-divider-controls"
          role="group"
          aria-label="Turn section collapse controls"
        >
          <button
            type="button"
            className="run-turn-activity-divider-toggle"
            data-direction="up"
            aria-expanded={true}
            aria-label="Collapse user message"
            title="Collapse user message"
          >
            <MinusIcon size={11} strokeWidth={2.4} aria-hidden="true" />
            <ChevronUpIcon
              className="run-turn-activity-divider-toggle-chevron"
              size={13}
              strokeWidth={2.3}
              aria-hidden="true"
            />
          </button>
          <button
            type="button"
            className="run-turn-activity-divider-toggle"
            data-direction="down"
            aria-expanded={true}
            aria-label="Collapse agent activity"
            title="Collapse agent activity"
          >
            <MinusIcon size={11} strokeWidth={2.4} aria-hidden="true" />
            <ChevronDownIcon
              className="run-turn-activity-divider-toggle-chevron"
              size={13}
              strokeWidth={2.3}
              aria-hidden="true"
            />
          </button>
        </div>
      </div>
      <div className="run-turn-view-body run-transcript run-transcript-claude">
        <RunningTool highlighted={highlighted} />
        <TranscriptMessage variant="assistant" highlighted={highlighted} ownedByActivity showAssistantAvatar>
          <p style={{ margin: 0 }}>
            I found the highlight hook and the active turn data attribute.
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
      <TurnThinkingMessage
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
      sendByCtrlEnter={false}
      hintSuffix=" / slash commands"
      disabled={readonly}
      canSubmit={!readonly}
      submitStatus={state === "streaming" ? "streaming" : undefined}
      onStop={() => {}}
      toolButtons={<ComposerToolButtons />}
    />
  );
}

function PortfolioTabs({ active }: { active: "transcript" | "turns" }) {
  return (
    <>
      <button className={`run-tab${active === "transcript" ? " run-tab-active" : ""}`} type="button" aria-pressed={active === "transcript"}>
        Transcript
      </button>
      <button className={`run-tab run-turns-trigger${active === "turns" ? " run-tab-active" : ""}`} type="button" aria-pressed={active === "turns"}>
        <ActivityIcon className="run-tab-icon" aria-hidden="true" />
        <span>Turns</span>
        <span className="run-shell-tasks-count" data-active="true">3</span>
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
  const [highlightTarget, setHighlightTarget] = useState<HighlightTarget>("activity");
  const [composerState, setComposerState] = useState<ComposerSpecimenState>("ready");
  const [activeSurface, setActiveSurface] = useState<ActiveSurface>("both");
  const transcriptActive = activeSurface === "transcript" || activeSurface === "both";
  const composerActive = activeSurface === "composer" || activeSurface === "both";
  const turnViewActive = highlightTarget === "activity";

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
              height: "min(720px, 78vh)",
              minHeight: 360,
              overflowX: "auto",
              overflowY: "auto",
            }}
          >
            <div style={{ minHeight: "100%", minWidth: 760 }}>
              <WorkspaceShell
                className="styleguide-transcript-focus-shell"
                title={
                  <>
                    <div className="run-header-title-row">
                      <button className="run-header-name-btn" type="button">
                        transcript-states
                      </button>
                      <span className="run-connection-pill" role="status" aria-live="polite">
                        <span className="run-connection-label">reconnecting</span>
                      </span>
                    </div>
                    <p className="run-header-sub">mocked codex gui session</p>
                  </>
                }
                tabs={<PortfolioTabs active={turnViewActive ? "turns" : "transcript"} />}
                body={
                  turnViewActive
                    ? <TurnViewSpecimen highlighted={highlightTarget === "activity"} />
                    : <TranscriptSpecimen highlightTarget={highlightTarget} />
                }
                bodyClassName={transcriptActive ? "styleguide-surface-active styleguide-transcript-surface-active" : undefined}
                bodyAriaLabel={turnViewActive ? "Turn view" : "Transcript"}
                floatingBetweenBodyAndComposer={
                  <button
                    className="run-transcript-edge-cue run-transcript-edge-cue-bottom run-transcript-edge-cue-pending"
                    type="button"
                    aria-label="2 new messages below"
                    title="2 new messages below"
                  >
                    <span className="run-transcript-edge-cue-rail" aria-hidden="true" />
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

        <section style={{ ...sectionStyle, display: "grid", gap: 12 }}>
          <h2 style={{ margin: 0, color: "var(--text-primary)", fontSize: "var(--text-lg)" }}>
            composer zoom pressure
          </h2>
          <div
            className="styleguide-composer-zoom-specimen"
            style={{
              width: "min(100%, 360px)",
              height: 360,
              border: "1px solid var(--border-soft)",
              borderRadius: "var(--radius-md)",
              background: "var(--bg-base)",
              overflow: "hidden",
            }}
          >
            <WorkspaceShell
              className="styleguide-transcript-focus-shell"
              title={
                <div className="run-header-title-row">
                  <button className="run-header-name-btn" type="button">
                    zoom-check
                  </button>
                </div>
              }
              body={<TranscriptSpecimen highlightTarget="user" />}
              bodyClassName="styleguide-surface-active styleguide-transcript-surface-active"
              bodyAriaLabel="Transcript"
              composer={<ComposerSpecimen state={composerState} active />}
            />
          </div>
        </section>
      </div>
    </div>
  );
}
