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
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import {
  CheckIcon,
  ChevronDownIcon,
  SendHorizontalIcon,
  XIcon,
} from "lucide-react";
import type { ChatStatus } from "ai";

// The composer is the single source of truth for the chat-style prompt box.
// One component is rendered on the home screen, on the unauthenticated demo
// landing, and inside an active session's run pane. The visual shell —
// PromptInput shell, permission-mode dropdown, hint, clear-X, submit button —
// is identical across all three callers; only the session-bound tool buttons
// (image attach, usage ring, rollout/test, slash and MCP menus) plug in via
// the `toolButtons` slot from inside the run pane. Keeping these surfaces in
// one component is what makes "type on the home screen and the same composer
// keeps going in the chat" true at the code level, not only visually.

export type RunComposerMode =
  | "default"
  | "acceptEdits"
  | "auto"
  | "bypassPermissions"
  | "plan";

export interface PermissionModeInfo {
  label: string;
  desc: string;
  /** Color of the dot rendered next to the pill label. */
  dotColor: string;
}

export const PERMISSION_MODE_INFO: Record<RunComposerMode, PermissionModeInfo> = {
  default: {
    label: "Default Mode",
    desc: "Ask before edits, agree to commands",
    dotColor: "#34d399",
  },
  acceptEdits: {
    label: "Accept Edits",
    desc: "Auto-approve file changes",
    dotColor: "#fbbf24",
  },
  auto: {
    label: "Auto",
    desc: "Auto-approve safe operations",
    dotColor: "#60a5fa",
  },
  bypassPermissions: {
    label: "Bypass Permissions",
    desc: "Run without permission prompts",
    dotColor: "#f87171",
  },
  plan: {
    label: "Plan Mode",
    desc: "Plan before execution",
    dotColor: "#a78bfa",
  },
};

export interface ChatComposerSubmitArgs {
  text: string;
  permissionMode: RunComposerMode;
}

export interface ChatComposerProps {
  /** Extra class on the PromptInput form — composes with `run-composer`. */
  className?: string;
  placeholder: string;
  onSubmit: (args: ChatComposerSubmitArgs) => void;
  permissionMode: RunComposerMode;
  onPermissionModeChange: (mode: RunComposerMode) => void;
  /** When true, plain Enter inserts a newline and only Ctrl/⌘+Enter submits. */
  sendByCtrlEnter: boolean;
  /** Appended after the Enter/Shift hint, e.g. " · / for slash commands". */
  hintSuffix?: string;
  /** Disables the textarea and submit (and stop) interactions. */
  disabled?: boolean;
  /**
   * Keeps the composer interactive but ignores submit attempts.
   * Used while a session is warming up so the text box still invites input.
   */
  canSubmit?: boolean;
  /** Disables the permission-mode dropdown and any other internal controls. */
  controlsDisabled?: boolean;
  /** PromptInputSubmit status — drives spinner/stop icon swaps while a turn streams. */
  submitStatus?: ChatStatus;
  onStop?: () => void;
  isStopping?: boolean;
  /**
   * Tool buttons rendered inside `PromptInputTools` after the permission-mode
   * pill. The run pane plugs in image-attach, usage ring, rollout/test, slash
   * menu, and MCP menu here. Home + demo leave this empty.
   */
  toolButtons?: ReactNode;
  /** Fires whenever the textarea's content changes — including programmatic clears. */
  onTextChange?: (text: string) => void;
}

/**
 * Shared chat composer used by the run pane, the home screen, and the demo
 * landing. Owns the universal shell (PromptInput + textarea + permission-mode
 * dropdown + hint + clear-X + submit) and exposes a `toolButtons` slot for
 * session-bound add-ons.
 */
export function ChatComposer({
  className,
  placeholder,
  onSubmit,
  permissionMode,
  onPermissionModeChange,
  sendByCtrlEnter,
  hintSuffix,
  disabled,
  canSubmit = true,
  controlsDisabled,
  submitStatus,
  onStop,
  isStopping,
  toolButtons,
  onTextChange,
}: ChatComposerProps) {
  // Internal mirror of the textarea's value so the hint can fade and the
  // clear-X can show/hide without making the textarea itself controlled
  // (PromptInput owns submission lifecycle, and turning the textarea into a
  // controlled input would fight its uncontrolled clear-on-submit reset).
  const [text, setText] = useState("");
  const composerRef = useRef<HTMLDivElement | null>(null);

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
      onSubmit({ text: message.text, permissionMode });
      // PromptInput auto-clears after a sync onSubmit; reflect that in our
      // mirror so the hint un-fades and the clear-X disappears.
      setText("");
      onTextChange?.("");
    },
    [canSubmit, onSubmit, onTextChange, permissionMode],
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

  const handleClear = useCallback(() => {
    const ta = composerRef.current?.querySelector("textarea") as
      | HTMLTextAreaElement
      | null;
    if (!ta) return;
    // Native setter + synthetic input event so React's internal change
    // tracking sees the new value and re-renders any consumers that
    // subscribe to the textarea (e.g. the run pane's slash/mention
    // detection).
    const setter = Object.getOwnPropertyDescriptor(
      window.HTMLTextAreaElement.prototype,
      "value",
    )?.set;
    setter?.call(ta, "");
    ta.dispatchEvent(new Event("input", { bubbles: true }));
    setText("");
    onTextChange?.("");
    ta.focus();
  }, [onTextChange]);

  const hintText = sendByCtrlEnter
    ? `⌘/Ctrl+Enter to send · Enter for new line${hintSuffix ?? ""}`
    : `Enter to send · Shift+Enter for new line${hintSuffix ?? ""}`;

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
        <PromptInputTextarea
          className="run-composer-textarea"
          placeholder={placeholder}
          disabled={disabled}
        />
        <PromptInputFooter className="run-composer-footer">
          <PromptInputTools className="run-composer-tools">
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <button
                  type="button"
                  className="run-mode-pill run-mode-pill-button"
                  disabled={disabled || controlsDisabled}
                  aria-label="Permission mode"
                >
                  <span
                    className="run-mode-dot"
                    aria-hidden="true"
                    style={{
                      background:
                        PERMISSION_MODE_INFO[permissionMode].dotColor,
                    }}
                  />
                  {PERMISSION_MODE_INFO[permissionMode].label}
                  <ChevronDownIcon
                    className="run-mode-chevron"
                    aria-hidden="true"
                  />
                </button>
              </DropdownMenuTrigger>
              <DropdownMenuContent
                side="top"
                align="start"
                className="run-mode-menu"
              >
                {(Object.keys(PERMISSION_MODE_INFO) as RunComposerMode[]).map(
                  (modeKey) => {
                    const info = PERMISSION_MODE_INFO[modeKey];
                    return (
                      <DropdownMenuItem
                        key={modeKey}
                        onSelect={() => onPermissionModeChange(modeKey)}
                      >
                        <span className="run-mode-menu-row">
                          <span className="run-mode-menu-meta">
                            <span
                              className="run-mode-menu-dot"
                              aria-hidden="true"
                              style={{ background: info.dotColor }}
                            />
                            <span className="run-mode-menu-label">
                              {info.label}
                            </span>
                            <span className="run-mode-menu-desc">
                              {info.desc}
                            </span>
                          </span>
                          {permissionMode === modeKey && (
                            <CheckIcon
                              className="run-mode-menu-check"
                              aria-hidden="true"
                            />
                          )}
                        </span>
                      </DropdownMenuItem>
                    );
                  },
                )}
              </DropdownMenuContent>
            </DropdownMenu>
            {toolButtons}
          </PromptInputTools>
          <span
            className={`run-composer-hint${text.length > 0 ? " run-composer-hint-faded" : ""}`}
          >
            {hintText}
          </span>
          {text.length > 0 && (
            <button
              type="button"
              className="run-composer-clear"
              aria-label="Clear input"
              onMouseDown={(e) => {
                // mousedown not click so blur doesn't fire before the click
                // reaches us (the run-pane palettes close on blur).
                e.preventDefault();
                handleClear();
              }}
            >
              <XIcon size={14} strokeWidth={2.2} aria-hidden="true" />
            </button>
          )}
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
        </PromptInputFooter>
      </PromptInput>
    </div>
  );
}
