import {
  BotIcon,
  CheckIcon,
  ChevronDownIcon,
  ListChecksIcon,
  Loader2Icon,
  MessageSquareIcon,
  SearchIcon,
  SquareTerminalIcon,
  WrenchIcon,
} from "lucide-react";
import type { ReactNode } from "react";
import { AgentAvatarIcon, getSessionAvatar } from "../sessionAvatars";
import {
  BackLink,
  captionStyle,
  pageTitleStyle,
  sectionStyle,
  styleguideShellStyle,
} from "./shared";

type TranscriptRole = "user" | "assistant" | "system";

const assistantAvatar = getSessionAvatar("turn-activity-thinking-prototype");

function ThinkingDots() {
  return (
    <span className="turn-activity-thinking-dots" aria-hidden="true">
      <span />
      <span />
      <span />
    </span>
  );
}

function TranscriptMessage({
  role,
  children,
  messageId,
  designState,
}: {
  role: TranscriptRole;
  children: ReactNode;
  messageId: string;
  designState?: string;
}) {
  const variant = role === "user" ? "user" : role === "system" ? "system" : "assistant";
  return (
    <div
      className="run-transcript-message"
      data-slot="message"
      data-variant={variant}
      data-role={variant}
      data-kind="message"
      data-message-id={messageId}
      data-design-component={designState ? "turn-activity-thinking" : undefined}
      data-design-state={designState}
      data-design-source={designState ? "frontend/src/styleguide/portfolio-turn-activity.tsx" : undefined}
    >
      {variant === "assistant" && (
        <span className="run-msg-ai-avatar" aria-hidden="true">
          <AgentAvatarIcon avatar={assistantAvatar} className="run-msg-ai-icon" />
        </span>
      )}
      {variant === "system" && (
        <span className="run-msg-system-avatar" aria-hidden="true">
          <BotIcon size={16} strokeWidth={2.1} />
        </span>
      )}
      <div className="run-transcript-message-content" data-slot="message-content">
        <div className="run-transcript-message-text" data-slot="message-text">
          {children}
        </div>
        <div className="run-msg-footer" data-always-visible="">
          <div className="run-msg-timings">
            <span className="run-msg-timing-row">now</span>
          </div>
        </div>
      </div>
      {variant === "user" && (
        <span className="run-msg-avatar">
          <span className="avatar" aria-hidden="true">NG</span>
        </span>
      )}
    </div>
  );
}

function MarkdownText({ children }: { children: ReactNode }) {
  return (
    <div className="run-markdown">
      <p>{children}</p>
    </div>
  );
}

function ToolPreview({
  icon,
  label,
  state,
  colorClass,
}: {
  icon: ReactNode;
  label: string;
  state: "running" | "completed" | "failed";
  colorClass: string;
}) {
  return (
    <div className="run-transcript-tool" data-slot="tool-item" data-kind="tool" data-state={state}>
      <div className="run-transcript-tool-connector" data-slot="tool-item-connector">
        <div className="run-transcript-tool-dot" data-slot="tool-item-dot" />
      </div>
      <div className="run-transcript-tool-content">
        <button
          type="button"
          className="run-transcript-tool-header"
          data-slot="tool-item-header"
          aria-expanded={false}
        >
          <span className="run-transcript-tool-icon" data-slot="tool-item-icon">
            <span className={`run-tool-icon-glyph ${colorClass}`} aria-hidden="true">
              {icon}
            </span>
          </span>
          <span className="run-transcript-tool-label" data-slot="tool-item-label">
            {label}
          </span>
          {state === "running" && (
            <Loader2Icon size={12} className="run-spin run-tool-spinner" aria-hidden="true" />
          )}
          <span className="run-transcript-tool-chevron" data-slot="tool-item-chevron">
            <ChevronDownIcon size={14} strokeWidth={2} className="run-chevron-icon" aria-hidden="true" />
          </span>
        </button>
      </div>
    </div>
  );
}

function ThinkingActivityMessage() {
  return (
    <TranscriptMessage role="assistant" messageId="thinking-active" designState="active">
      <div className="run-markdown turn-activity-thinking-copy">
        <p className="turn-activity-thinking-title">
          Codex is thinking
          <ThinkingDots />
        </p>
        <p className="turn-activity-thinking-subtitle">
          Reading the current transcript renderer and preparing a static hot-swap.
        </p>
      </div>
      <div className="run-transcript-tools turn-activity-thinking-tools" data-slot="tool-group" data-state="running">
        <button type="button" className="run-transcript-tools-header" aria-expanded={true}>
          <span className="run-transcript-tools-icon" title="Turn activity" aria-label="Turn activity">
            <WrenchIcon size={14} strokeWidth={2} aria-hidden="true" />
          </span>
          <span className="run-transcript-tools-label">3 activity updates - 1 running</span>
          <Loader2Icon size={12} className="run-spin run-tool-spinner" aria-hidden="true" />
          <span className="run-transcript-tools-chevron">
            <ChevronDownIcon size={14} className="run-chevron-icon" aria-hidden="true" />
          </span>
        </button>
        <div className="run-transcript-tools-body">
          <ToolPreview
            icon={<SearchIcon size={14} strokeWidth={2} />}
            label="Read docs/product-inspirations.md"
            state="completed"
            colorClass="tool-color-search"
          />
          <ToolPreview
            icon={<SquareTerminalIcon size={14} strokeWidth={2} />}
            label="Inspect RunMessages and RunTurnActivityGroup"
            state="completed"
            colorClass="tool-color-bash"
          />
          <ToolPreview
            icon={<ListChecksIcon size={14} strokeWidth={2} />}
            label="Hot-swap tank-operator-slot-1"
            state="running"
            colorClass="tool-color-todo"
          />
        </div>
      </div>
    </TranscriptMessage>
  );
}

function SettledActivityMessage() {
  return (
    <TranscriptMessage role="assistant" messageId="thinking-settled" designState="settled">
      <div className="run-markdown turn-activity-thinking-copy">
        <p className="turn-activity-thinking-title">
          <CheckIcon size={15} strokeWidth={2.1} aria-hidden="true" />
          Codex thought for 42s
        </p>
        <p className="turn-activity-thinking-subtitle">
          2 tools, 1 file changed. Expandable detail would use the same tool rows above.
        </p>
      </div>
    </TranscriptMessage>
  );
}

function NeedsInputActivityMessage() {
  return (
    <TranscriptMessage role="system" messageId="thinking-input" designState="needs-input">
      <div className="run-markdown turn-activity-thinking-copy">
        <p className="turn-activity-thinking-title">
          <MessageSquareIcon size={15} strokeWidth={2.1} aria-hidden="true" />
          Codex needs input
        </p>
        <p className="turn-activity-thinking-subtitle">
          Choose whether this should replace or supplement the existing Turn activity row.
        </p>
      </div>
    </TranscriptMessage>
  );
}

export function StyleguidePortfolioTurnActivity() {
  return (
    <div style={styleguideShellStyle}>
      <div style={{ maxWidth: 980 }}>
        <BackLink />
        <h1 style={pageTitleStyle}>portfolio scene: turn activity</h1>
        <p style={captionStyle}>
          Prototype for a Slack/Discord-style thinking indicator placed inside
          the same transcript shell, message grid, avatar column, and tool rows
          the production chat pane uses today.
        </p>
        <section style={sectionStyle}>
          <div className="turn-activity-production-frame">
            <main className="run-main turn-activity-production-main" aria-label="Transcript">
              <div className="run-transcript run-transcript-claude turn-activity-production-transcript" data-slot="root">
                <TranscriptMessage role="user" messageId="user-request">
                  <MarkdownText>
                    Can you set up a first pass at the new turn activity treatment?
                  </MarkdownText>
                </TranscriptMessage>
                <ThinkingActivityMessage />
                <TranscriptMessage role="assistant" messageId="assistant-answer">
                  <MarkdownText>
                    I set up the prototype route and wired it into the styleguide.
                  </MarkdownText>
                </TranscriptMessage>
                <SettledActivityMessage />
                <NeedsInputActivityMessage />
              </div>
            </main>
          </div>
        </section>
      </div>
    </div>
  );
}
