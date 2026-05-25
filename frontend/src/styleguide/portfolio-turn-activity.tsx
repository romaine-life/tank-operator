import {
  BotIcon,
  CheckIcon,
  ChevronDownIcon,
  ListChecksIcon,
  MessageSquareIcon,
  SearchIcon,
  SquareTerminalIcon,
} from "lucide-react";
import {
  BackLink,
  captionStyle,
  pageTitleStyle,
  sectionStyle,
  styleguideShellStyle,
} from "./shared";

const activitySteps = [
  {
    icon: SearchIcon,
    label: "Reading docs/product-inspirations.md",
    meta: "durable history and live delivery split",
  },
  {
    icon: SquareTerminalIcon,
    label: "Checking transcript activity components",
    meta: "frontend/src/App.tsx",
  },
  {
    icon: ListChecksIcon,
    label: "Preparing a hot-swap test",
    meta: "tank-operator-slot-1",
  },
];

function ThinkingDots() {
  return (
    <span className="turn-activity-thinking-dots" aria-hidden="true">
      <span />
      <span />
      <span />
    </span>
  );
}

function ThinkingCard() {
  return (
    <details
      className="turn-activity-thinking-card is-active"
      open
      data-design-component="turn-activity-thinking"
      data-design-state="active"
      data-design-source="frontend/src/styleguide/portfolio-turn-activity.tsx"
    >
      <summary className="turn-activity-thinking-summary">
        <span className="turn-activity-thinking-avatar" aria-hidden="true">
          <BotIcon size={15} strokeWidth={2} />
        </span>
        <span className="turn-activity-thinking-copy">
          <span className="turn-activity-thinking-label">
            Codex is thinking
            <ThinkingDots />
          </span>
          <span className="turn-activity-thinking-meta">1m 18s - working in /workspace/tank-operator</span>
        </span>
        <ChevronDownIcon className="turn-activity-thinking-chevron" size={15} strokeWidth={2} aria-hidden="true" />
      </summary>
      <div className="turn-activity-thinking-body">
        {activitySteps.map((step) => {
          const Icon = step.icon;
          return (
            <div className="turn-activity-thinking-step" key={step.label}>
              <span className="turn-activity-thinking-step-icon" aria-hidden="true">
                <Icon size={14} strokeWidth={2} />
              </span>
              <span className="turn-activity-thinking-step-text">
                <span>{step.label}</span>
                <span>{step.meta}</span>
              </span>
            </div>
          );
        })}
      </div>
    </details>
  );
}

function SettledActivityCard() {
  return (
    <details
      className="turn-activity-thinking-card is-settled"
      data-design-component="turn-activity-thinking"
      data-design-state="settled"
      data-design-source="frontend/src/styleguide/portfolio-turn-activity.tsx"
    >
      <summary className="turn-activity-thinking-summary">
        <span className="turn-activity-thinking-avatar" aria-hidden="true">
          <CheckIcon size={15} strokeWidth={2} />
        </span>
        <span className="turn-activity-thinking-copy">
          <span className="turn-activity-thinking-label">Codex thought for 42s</span>
          <span className="turn-activity-thinking-meta">2 tools, 1 file changed</span>
        </span>
        <ChevronDownIcon className="turn-activity-thinking-chevron" size={15} strokeWidth={2} aria-hidden="true" />
      </summary>
      <div className="turn-activity-thinking-body">
        <div className="turn-activity-thinking-step">
          <span className="turn-activity-thinking-step-icon" aria-hidden="true">
            <SquareTerminalIcon size={14} strokeWidth={2} />
          </span>
          <span className="turn-activity-thinking-step-text">
            <span>Ran frontend build</span>
            <span>npm run build</span>
          </span>
        </div>
      </div>
    </details>
  );
}

function NeedsInputActivityCard() {
  return (
    <div
      className="turn-activity-thinking-card is-input"
      data-design-component="turn-activity-thinking"
      data-design-state="needs-input"
      data-design-source="frontend/src/styleguide/portfolio-turn-activity.tsx"
    >
      <div className="turn-activity-thinking-summary">
        <span className="turn-activity-thinking-avatar" aria-hidden="true">
          <MessageSquareIcon size={15} strokeWidth={2} />
        </span>
        <span className="turn-activity-thinking-copy">
          <span className="turn-activity-thinking-label">Codex needs input</span>
          <span className="turn-activity-thinking-meta">Choose whether this should replace or supplement turn activity.</span>
        </span>
      </div>
    </div>
  );
}

export function StyleguidePortfolioTurnActivity() {
  return (
    <div style={styleguideShellStyle}>
      <div style={{ maxWidth: 980 }}>
        <BackLink />
        <h1 style={pageTitleStyle}>portfolio scene: turn activity</h1>
        <p style={captionStyle}>
          Prototype for a Slack/Discord-style thinking indicator that can carry
          durable turn activity without making the transcript feel like a log
          viewer.
        </p>
        <section style={sectionStyle}>
          <div className="turn-activity-lab-frame">
            <div className="turn-activity-lab-transcript" aria-label="Turn activity thinking prototype">
              <div className="turn-activity-lab-message is-user">
                <span className="turn-activity-lab-author">You</span>
                <p>Can you set up a first pass at the new turn activity treatment?</p>
              </div>
              <ThinkingCard />
              <div className="turn-activity-lab-message is-assistant">
                <span className="turn-activity-lab-author">Codex</span>
                <p>I set up the prototype route and wired it into the styleguide.</p>
              </div>
              <SettledActivityCard />
              <NeedsInputActivityCard />
            </div>
            <aside className="turn-activity-lab-notes" aria-label="Prototype states">
              <div>
                <span className="turn-activity-lab-kicker">Active</span>
                <p>Inline presence row, low contrast, with animated typing dots and expandable durable detail.</p>
              </div>
              <div>
                <span className="turn-activity-lab-kicker">Settled</span>
                <p>Collapsed history keeps a compact timing and tool-count summary.</p>
              </div>
              <div>
                <span className="turn-activity-lab-kicker">Needs input</span>
                <p>Same footprint, warmer state color, no activity expansion unless there is detail to inspect.</p>
              </div>
            </aside>
          </div>
        </section>
      </div>
    </div>
  );
}
