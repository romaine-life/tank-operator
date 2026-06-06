import { useState } from "react";
import {
  ChevronDownIcon,
  ChevronUpIcon,
  CircleCheckIcon,
  SquareTerminalIcon,
} from "lucide-react";
import { AgentAvatarIcon, getSessionAvatarByID } from "../sessionAvatars";
import {
  BackLink,
  captionStyle,
  pageTitleStyle,
  sectionStyle,
  styleguideShellStyle,
} from "./shared";

function MockUserTurn() {
  return (
    <div className="collapsed-turn-mock-message is-user">
      <span className="collapsed-turn-mock-user-avatar" aria-hidden="true">
        NG
      </span>
      <div className="collapsed-turn-mock-message-bubble">
        Make the activity collapse feel like metadata, not like another message.
      </div>
    </div>
  );
}

function MockAgentActivity() {
  return (
    <div className="collapsed-turn-mock-activity" aria-label="Agent activity">
      <div className="collapsed-turn-mock-tool">
        <span className="collapsed-turn-mock-tool-icon" aria-hidden="true">
          <SquareTerminalIcon size={14} />
        </span>
        <span>bash: inspect turn activity projection</span>
        <span className="collapsed-turn-mock-tool-status">done</span>
      </div>
      <div className="collapsed-turn-mock-note">
        Checked the turn shell and final-answer split before rendering the
        response.
      </div>
    </div>
  );
}

function MockAgentResponse() {
  const avatar = getSessionAvatarByID("jp1-malcolm");
  return (
    <div className="collapsed-turn-mock-message is-agent">
      {avatar && (
        <span className="collapsed-turn-mock-agent-avatar" aria-hidden="true">
          <AgentAvatarIcon avatar={avatar} className="run-msg-ai-icon" />
        </span>
      )}
      <div className="collapsed-turn-mock-message-bubble">
        This keeps the transcript focused on the answer while leaving the work
        trace available behind the divider.
      </div>
    </div>
  );
}

function ActivityDivider({
  expanded,
  onToggle,
}: {
  expanded: boolean;
  onToggle: () => void;
}) {
  const tooltip = expanded ? "Hide agent activity" : "Show agent activity";
  return (
    <div className="collapsed-turn-activity-divider">
      <button
        type="button"
        className="collapsed-turn-activity-toggle"
        data-direction={expanded ? "up" : "down"}
        aria-label={tooltip}
        aria-expanded={expanded}
        title={tooltip}
        onClick={onToggle}
      >
        {expanded ? (
          <ChevronUpIcon size={15} strokeWidth={2.2} aria-hidden="true" />
        ) : (
          <ChevronDownIcon size={15} strokeWidth={2.2} aria-hidden="true" />
        )}
      </button>
    </div>
  );
}

export function StyleguideCollapsedTurnActivity() {
  const [expanded, setExpanded] = useState(false);

  return (
    <div style={styleguideShellStyle}>
      <div style={{ maxWidth: 980 }}>
        <BackLink />
        <h1 style={pageTitleStyle}>collapsed turn activity divider</h1>
        <p style={{ ...captionStyle, maxWidth: "72ch" }}>
          Interactive mockup for the minimal affordance: the existing separator
          between the user turn and agent-side activity gets only a circular
          chevron button. Hover the button for the tooltip; click to expand.
        </p>

        <section style={{ ...sectionStyle, display: "grid", gap: 16 }}>
          <div className="collapsed-turn-mock-frame">
            <div className="collapsed-turn-mock-head">
              <span>Turns</span>
              <span className="collapsed-turn-mock-state">
                <CircleCheckIcon size={13} aria-hidden="true" />
                complete
              </span>
            </div>
            <div className="collapsed-turn-mock-body">
              <MockUserTurn />
              <ActivityDivider
                expanded={expanded}
                onToggle={() => setExpanded((value) => !value)}
              />
              {expanded && <MockAgentActivity />}
              <MockAgentResponse />
            </div>
          </div>
        </section>
      </div>
    </div>
  );
}
