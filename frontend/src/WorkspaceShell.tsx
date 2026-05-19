import type {
  CSSProperties,
  ClipboardEventHandler,
  DragEventHandler,
  ReactNode,
  Ref,
} from "react";

// The single workspace scaffold rendered in App's main pane, regardless of
// whether a session is active. Both the home starter and the per-session
// chat pane fill the same structural slots — body and composer, with an
// optional workspace header — so the user types in the same composer at the
// same y-coordinate and the active-session pane can still expose its
// session-bound controls.
//
// Owning these in one component is what makes "the home is an empty state
// of the chat surface" structurally true rather than visually approximated.
// Per docs/quality-timeframes.md: complete architecture, single durable
// source of truth, no parallel scaffolds.

export interface WorkspaceShellProps {
  /** Extra class appended to `run-panel` for caller-specific styling. */
  className?: string;
  /** Inline style (used by ChatPane for chat font scale). */
  style?: CSSProperties;
  /**
   * Header title slot — typically the session-name editor for an active
   * session or the pre-create name editor for the home starter.
   */
  title?: ReactNode;
  /**
   * Tab nav slot — Files / Settings / Help (plus the Back-to-chat row when
   * inside the run pane). Callers may pass disabled tabs when the workspace
   * chrome should be visible before session-bound targets exist.
   */
  tabs?: ReactNode;
  /**
   * Floating UI between the body and the composer footer — status pill,
   * scroll-to-top, scroll-to-bottom. Optional; the home starter doesn't
   * supply any.
   */
  floatingBetweenBodyAndComposer?: ReactNode;
  /**
   * Main content slot — `run-main`'s inner. ChatPane fills this with the
   * transcript / files / settings / help view; the home starter fills it
   * with the configuration + launchers + sessions grid.
   */
  body: ReactNode;
  /** Modifier appended to `run-main` (e.g. status-driven classes). */
  bodyClassName?: string;
  /** Direct ref to the scrolling `<main>` element. */
  bodyRef?: Ref<HTMLElement>;
  /**
   * Composer footer slot — the `<ChatComposer>` plus any above-composer
   * UI (palettes, queued messages, attachments preview). Hidden when
   * `composerVisible === false` (e.g. ChatPane on the Files tab).
   */
  composer: ReactNode;
  /**
   * Rendered above the composer, inside the same wrap. Slash / mention /
   * MCP palettes, queued-follow-ups, and attachment chips live here in
   * the run pane; the home starter uses it for its pending-attachments
   * preview.
   */
  composerAbove?: ReactNode;
  composerVisible?: boolean;
  composerWrapRef?: Ref<HTMLElement>;
  composerWrapClassName?: string;
  composerWrapStyle?: CSSProperties;
  /** Drag/drop handlers, hoisted from the original `run-composer-wrap`. */
  onComposerWrapDragOver?: DragEventHandler<HTMLElement>;
  onComposerWrapDragLeave?: DragEventHandler<HTMLElement>;
  onComposerWrapDrop?: DragEventHandler<HTMLElement>;
  onComposerWrapPaste?: ClipboardEventHandler<HTMLElement>;
}

export function WorkspaceShell({
  className,
  style,
  title,
  tabs,
  floatingBetweenBodyAndComposer,
  body,
  bodyClassName,
  bodyRef,
  composer,
  composerAbove,
  composerVisible = true,
  composerWrapRef,
  composerWrapClassName,
  composerWrapStyle,
  onComposerWrapDragOver,
  onComposerWrapDragLeave,
  onComposerWrapDrop,
  onComposerWrapPaste,
}: WorkspaceShellProps) {
  const hasHeader = title != null || tabs != null;

  return (
    <section className={["run-panel", className].filter(Boolean).join(" ")} style={style}>
      {hasHeader && (
        <header className="run-header">
          {title != null && <div className="run-header-title">{title}</div>}
          {tabs != null && (
            <nav className="run-tabs" aria-label="Session actions">
              {tabs}
            </nav>
          )}
        </header>
      )}

      <main
        className={["run-main", bodyClassName].filter(Boolean).join(" ")}
        ref={bodyRef}
      >
        {body}
      </main>

      {floatingBetweenBodyAndComposer}

      {composerVisible && (
        <footer
          className={["run-composer-wrap", composerWrapClassName]
            .filter(Boolean)
            .join(" ")}
          ref={composerWrapRef}
          style={composerWrapStyle}
          onDragOver={onComposerWrapDragOver}
          onDragLeave={onComposerWrapDragLeave}
          onDrop={onComposerWrapDrop}
          onPaste={onComposerWrapPaste}
        >
          {composerAbove}
          {composer}
        </footer>
      )}
    </section>
  );
}
