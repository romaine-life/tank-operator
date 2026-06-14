// Visual specimen for the Turns-view AskUserQuestion heading. The "Question
// N of M" indicator used to render as a full-width orphaned banner pinned above
// the question column (.run-turn-question-page-head). It now renders as a
// system-user message: system avatar in the gutter, label + position counter in
// the message column, scrolling in-flow with the rest of the question body. This
// mirrors how session.status banners, RunMetaBlock status lines, and the
// background-wake prompt ("when the timer goes off") speak through the shared
// system-avatar message frame instead of floating with no author.
//
// Like the other transcript specimens (collapsed-turn-activity, portfolio-
// transcript), the DOM here is hand-rolled with the real classes so the frame
// renders under the production CSS. The live component is App.tsx
// RunQuestionHeadingMessage.
import { BotIcon } from "lucide-react";
import { AgentAvatarIcon, getSessionAvatarByID } from "../sessionAvatars";
import {
  BackLink,
  captionStyle,
  pageTitleStyle,
  sectionStyle,
  showcaseFrameStyle,
  styleguideShellStyle,
} from "./shared";

function QuestionHeadingMessage({
  answered = false,
  questionIndex,
  questionCount,
}: {
  answered?: boolean;
  questionIndex?: number;
  questionCount?: number;
}) {
  const hasPosition =
    typeof questionIndex === "number" && typeof questionCount === "number";
  const label =
    !hasPosition && (questionCount ?? 0) > 1 ? "Questions" : "Question";
  const counter = hasPosition ? `${questionIndex} of ${questionCount}` : null;
  return (
    <div
      className="run-transcript-message"
      data-slot="message"
      data-variant="system"
      data-role="system"
      data-kind="question-heading"
      data-answered={answered ? "true" : "false"}
    >
      <span className="run-msg-system-avatar" aria-hidden="true">
        <BotIcon size={16} strokeWidth={2.1} />
      </span>
      <div
        className="run-transcript-message-content"
        data-slot="message-content"
      >
        <div className="run-transcript-message-text" data-slot="message-text">
          <span className="run-question-heading">
            <span className="run-question-heading-label">{label}</span>
            {counter && (
              <span className="run-question-heading-count">{counter}</span>
            )}
          </span>
        </div>
      </div>
    </div>
  );
}

function MockAssistantMessage() {
  const avatar = getSessionAvatarByID("jp1-malcolm");
  return (
    <div
      className="run-transcript-message"
      data-slot="message"
      data-variant="assistant"
      data-role="assistant"
    >
      {avatar && (
        <span className="run-msg-ai-avatar" aria-hidden="true">
          <AgentAvatarIcon avatar={avatar} className="run-msg-ai-icon" />
        </span>
      )}
      <div
        className="run-transcript-message-content"
        data-slot="message-content"
      >
        <div className="run-transcript-message-text" data-slot="message-text">
          <p>
            Got it — I&rsquo;ll trigger the user-input tool now with a concise
            Santa Claus question and a few options so you can pick your preferred
            direction.
          </p>
        </div>
      </div>
    </div>
  );
}

const QUESTION_OPTIONS = [
  {
    label: "Cultural traditions (Recommended)",
    desc: "Explore how Santa is portrayed across different cultures and holidays.",
    selected: true,
  },
  {
    label: "History and origins",
    desc: "Focus on where Santa Claus traditions began and how they evolved.",
    selected: false,
  },
  {
    label: "Symbolism and pop culture",
    desc: "Look at modern media, marketing, and cultural symbolism around Santa.",
    selected: false,
  },
];

function MockQuestionCard() {
  return (
    <div
      className="run-tool-body run-tool-ask"
      data-answered="false"
      data-dismissed="false"
    >
      <div className="run-tool-ask-question">
        <span className="run-tool-ask-chip">Santa topic</span>
        <p className="run-tool-ask-text">
          Which Santa Claus topic should we focus on first?
        </p>
        <div
          className="run-tool-ask-options"
          role="radiogroup"
          aria-label="Which Santa Claus topic should we focus on first?"
        >
          {QUESTION_OPTIONS.map((opt) => (
            <button
              key={opt.label}
              type="button"
              className={
                "run-tool-ask-option" +
                (opt.selected ? " run-tool-ask-option-selected" : "")
              }
              aria-pressed={opt.selected}
            >
              <span
                className="run-tool-ask-option-marker"
                aria-hidden="true"
                data-selected={opt.selected ? "true" : "false"}
              >
                {opt.selected ? "●" : "○"}
              </span>
              <span className="run-tool-ask-option-body">
                <span className="run-tool-ask-option-label">{opt.label}</span>
                <span className="run-tool-ask-option-desc">{opt.desc}</span>
              </span>
            </button>
          ))}
        </div>
        <label className="run-tool-ask-notes-label">
          <span>Say something else (optional)</span>
          <textarea
            className="run-tool-ask-notes"
            rows={2}
            placeholder="Add a free-form reply or extra context…"
            readOnly
          />
        </label>
      </div>
    </div>
  );
}

export function StyleguideQuestionHeading() {
  return (
    <div style={styleguideShellStyle}>
      <div style={{ maxWidth: 980 }}>
        <BackLink />
        <h1 style={pageTitleStyle}>ask-user-question heading</h1>
        <p style={{ ...captionStyle, maxWidth: "72ch" }}>
          The Turns-view question page heading (&ldquo;Question N of M&rdquo;)
          now renders as a system-user message — system avatar in the gutter,
          label + position counter in the message column — instead of a
          full-width orphaned banner pinned above the column. It speaks through
          the same system-avatar frame as session.status banners, RunMetaBlock
          status lines, and the background-wake prompt that lands &ldquo;when the
          timer goes off.&rdquo;
        </p>

        <section style={sectionStyle}>
          <div style={showcaseFrameStyle}>
            <div className="run-transcript run-transcript-claude">
              <QuestionHeadingMessage questionIndex={1} questionCount={1} />
              <MockAssistantMessage />
              <MockQuestionCard />
            </div>
          </div>
        </section>

        <section style={sectionStyle}>
          <p style={{ ...captionStyle, maxWidth: "72ch" }}>
            Multi-question set (position counter) and answered state:
          </p>
          <div style={showcaseFrameStyle}>
            <div className="run-transcript run-transcript-claude">
              <QuestionHeadingMessage questionIndex={2} questionCount={3} />
              <QuestionHeadingMessage
                questionIndex={1}
                questionCount={1}
                answered
              />
            </div>
          </div>
        </section>
      </div>
    </div>
  );
}
