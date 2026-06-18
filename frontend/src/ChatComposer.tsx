import { useCallback, useEffect, useRef, useState } from "react";
import type { FormEventHandler, ReactNode } from "react";
import {
  PromptInput,
  PromptInputFooter,
  PromptInputSubmit,
  PromptInputTextarea,
  PromptInputTools,
  type PromptInputMessage,
} from "@/components/ai-elements/prompt-input";
import { SendHorizontalIcon } from "lucide-react";
import type { ChatStatus } from "ai";

// The composer is the single source of truth for the chat-style prompt box.
// One component is rendered on the home screen, on the unauthenticated demo
// landing, and inside an active session's run pane. The visual shell —
// PromptInput shell, hint, submit button — is identical across all three
// callers; only the session-bound tool buttons
// (image attach, usage ring, rollout/test, slash and MCP menus) plug in via
// the `toolButtons` slot from inside the run pane. Keeping these surfaces in
// one component is what makes "type on the home screen and the same composer
// keeps going in the chat" true at the code level, not only visually.

export interface ChatComposerSubmitArgs {
  text: string;
}

export interface ChatComposerProps {
  /** Extra class on the PromptInput form — composes with `run-composer`. */
  className?: string;
  placeholder: string;
  onSubmit: (args: ChatComposerSubmitArgs) => void;
  /** When true, plain Enter inserts a newline and only Ctrl/⌘+Enter submits. */
  sendByCtrlEnter: boolean;
  /** Appended after the Enter/Shift hint, e.g. " · / for slash commands". */
  hintSuffix?: string;
  /** Replaces the default send/newline hint when the composer is informational. */
  hintOverride?: string;
  /** Hides the send/newline hint row when footer space is reserved for controls. */
  hideHint?: boolean;
  /** Disables the textarea and submit (and stop) interactions. */
  disabled?: boolean;
  /**
   * Keeps the composer interactive but ignores submit attempts.
   * Used while a session is warming up so the text box still invites input.
   */
  canSubmit?: boolean;
  /** PromptInputSubmit status — drives spinner/stop icon swaps while a turn streams. */
  submitStatus?: ChatStatus;
  onStop?: () => void;
  isStopping?: boolean;
  /**
   * Tool buttons rendered inside `PromptInputTools`. The run pane plugs in
   * image-attach, usage ring, rollout/test, slash menu, and MCP menu here.
   * Home + demo leave this empty.
   */
  toolButtons?: ReactNode;
  /** Seeds the uncontrolled textarea for static portfolio/demo specimens. */
  initialText?: string;
  /** Fires whenever the textarea's content changes — including programmatic clears. */
  onTextChange?: (text: string) => void;
  /**
   * When set, the composer is bound to a pending AskUserQuestion as the single
   * answer input — so the question screen never shows two text boxes. The send
   * button becomes a labelled Submit, Enter submits the assembled answer, the
   * placeholder teaches the mechanic, and the selected option chips render
   * above the field so it is obvious the picker fills this one box.
   */
  answerMode?: {
    label: string;
    canSubmit: boolean;
    onSubmit: () => void;
  };
}

function ComposerTextPreview({ text }: { text: string }) {
  const slashMatch = text.match(/^(\/[^\s/]+)/);
  if (!slashMatch) return null;
  const command = slashMatch[1] ?? "";
  return (
    <span className="run-composer-slash-token">{command}</span>
  );
}

/**
 * Shared chat composer used by the run pane, the home screen, and the demo
 * landing. Owns the universal shell (PromptInput + textarea + hint + submit)
 * and exposes a `toolButtons` slot for session-bound add-ons.
 */
export function ChatComposer({
  className,
  placeholder,
  onSubmit,
  sendByCtrlEnter,
  hintSuffix,
  hintOverride,
  hideHint,
  disabled,
  canSubmit = true,
  submitStatus,
  onStop,
  isStopping,
  toolButtons,
  initialText,
  onTextChange,
  answerMode,
}: ChatComposerProps) {
  // Internal mirror of the textarea's value so the hint can fade without
  // making the textarea itself controlled
  // (PromptInput owns submission lifecycle, and turning the textarea into a
  // controlled input would fight its uncontrolled clear-on-submit reset).
  const [text, setText] = useState(initialText ?? "");
  const composerRef = useRef<HTMLDivElement | null>(null);
  const seededInitialTextRef = useRef(false);

  useEffect(() => {
    if (!initialText || seededInitialTextRef.current) return;
    const ta = composerRef.current?.querySelector("textarea") as
      | HTMLTextAreaElement
      | null;
    if (!ta) return;
    seededInitialTextRef.current = true;
    const setter = Object.getOwnPropertyDescriptor(
      window.HTMLTextAreaElement.prototype,
      "value",
    )?.set;
    setter?.call(ta, initialText);
    ta.dispatchEvent(new Event("input", { bubbles: true }));
    setText(initialText);
  }, [initialText]);

  // Apply the requested Enter-vs-Ctrl+Enter behavior. The textarea's own
  // keydown logic in PromptInput.tsx submits on Enter (no shift) by default,
  // which matches `sendByCtrlEnter: false`. When the user has flipped the
  // preference, swallow plain-Enter keystrokes at the capture phase so the
  // textarea inserts a newline instead, and let Ctrl/⌘+Enter fall through to
  // the form-submit path.
  useEffect(() => {
    if (!sendByCtrlEnter) return;
    const wrap = composerRef.current;
    if (!wrap) return;
    const onKey = (event: Event) => {
      const e = event as KeyboardEvent;
      if (e.key !== "Enter") return;
      const target = e.target as HTMLElement | null;
      if (target?.tagName !== "TEXTAREA") return;
      if (e.shiftKey || e.ctrlKey || e.metaKey) return;
      // Stop only — preventDefault would block the newline we want.
      e.stopPropagation();
    };
    wrap.addEventListener("keydown", onKey, true);
    return () => wrap.removeEventListener("keydown", onKey, true);
  }, [sendByCtrlEnter]);

  const handleSubmit = useCallback(
    (message: PromptInputMessage) => {
      if (!canSubmit) return;
      if (answerMode) {
        // Answering a pending question: Enter submits the assembled answer
        // (current selections + this typed text), not a new chat turn.
        if (!answerMode.canSubmit) return;
        answerMode.onSubmit();
        setText("");
        onTextChange?.("");
        return;
      }
      onSubmit({ text: message.text });
      // PromptInput auto-clears after a sync onSubmit; reflect that in our
      // mirror so the hint un-fades.
      setText("");
      onTextChange?.("");
    },
    [answerMode, canSubmit, onSubmit, onTextChange],
  );

  const handleSubmitCapture = useCallback<FormEventHandler<HTMLFormElement>>(
    (event) => {
      if (canSubmit) return;
      // PromptInput resets the underlying form before its own submit handler
      // runs. Stopping propagation in capture keeps the warm-up text in place
      // until the session is ready to accept a real turn.
      event.preventDefault();
      event.stopPropagation();
    },
    [canSubmit],
  );

  const hintText = hintOverride
    ?? (sendByCtrlEnter
      ? `⌘/Ctrl+Enter to send · Enter for new line${hintSuffix ?? ""}`
      : `Enter to send · Shift+Enter for new line${hintSuffix ?? ""}`);

  return (
    <div
      ref={composerRef}
      className="chat-composer-shell"
      onInput={(e) => {
        const target = e.target as HTMLElement | null;
        if (target?.tagName !== "TEXTAREA") return;
        const value = (target as HTMLTextAreaElement).value;
        setText((prev) => {
          if (prev === value) return prev;
          onTextChange?.(value);
          return value;
        });
      }}
    >
      <PromptInput
        onSubmit={handleSubmit}
        onSubmitCapture={handleSubmitCapture}
        className={["run-composer", className].filter(Boolean).join(" ")}
      >
        <div className="run-composer-textarea-wrap">
          {text.match(/^\/[^\s/]+/) && (
            <div className="run-composer-text-preview" aria-hidden="true">
              <ComposerTextPreview text={text} />
            </div>
          )}
          <PromptInputTextarea
            className={[
              "run-composer-textarea",
              text.match(/^\/[^\s/]+/) ? "run-composer-textarea-tokenized" : "",
            ].filter(Boolean).join(" ")}
            placeholder={placeholder}
            disabled={disabled}
          />
        </div>
        <PromptInputFooter className="run-composer-footer">
          <PromptInputTools className="run-composer-tools">
            {toolButtons}
          </PromptInputTools>
          {!hideHint && (
            <span
              className={`run-composer-hint${text.length > 0 ? " run-composer-hint-faded" : ""}`}
            >
              {hintText}
            </span>
          )}
          {answerMode ? (
            <button
              type="button"
              className="run-answer-submit"
              onClick={() => {
                if (answerMode.canSubmit) answerMode.onSubmit();
              }}
              disabled={!answerMode.canSubmit}
              aria-label={answerMode.label}
              style={{
                display: "inline-flex",
                alignItems: "center",
                height: "32px",
                width: "auto",
                padding: "0 16px",
                borderRadius: "8px",
                fontSize: "13px",
                fontWeight: 600,
                whiteSpace: "nowrap",
                color: answerMode.canSubmit
                  ? "rgb(255,255,255)"
                  : "rgb(150,150,150)",
                background: answerMode.canSubmit
                  ? "rgb(37,99,235)"
                  : "rgba(255,255,255,0.06)",
                border: "none",
                cursor: answerMode.canSubmit ? "pointer" : "not-allowed",
              }}
            >
              {answerMode.label}
            </button>
          ) : (
            <PromptInputSubmit
              className="run-submit-btn"
              status={submitStatus}
              onStop={onStop}
              isStopping={isStopping}
              disabled={disabled}
            >
              {submitStatus ? undefined : (
                <SendHorizontalIcon
                  className="run-submit-icon"
                  aria-hidden="true"
                />
              )}
            </PromptInputSubmit>
          )}
        </PromptInputFooter>
      </PromptInput>
    </div>
  );
}
