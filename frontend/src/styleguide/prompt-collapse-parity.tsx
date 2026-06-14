// Regression fixture for the Turns-view prompt-context collapse/expand polish
// (romaine-life/tank-operator). Collapsing a one-line user prompt must be a
// pure CSS restyle of ONE stable DOM tree:
//
//   * the footer cluster (arrow / copy / timestamp) must not move,
//   * the "open in transcript" arrow must stay muted (not the blue underlined
//     markdown-link color it would inherit inside the message text), and
//   * the prompt text + footer must NOT remount on toggle — a remount is what
//     produced the flicker the durable fix removes.
//
// The DOM here mirrors RunMessageBubble's real output (App.tsx) for a
// `canonicalMessage={false}` user prompt: a single `.run-plain-message-text`
// element plus one `.run-msg-footer`, both rendered unconditionally inside the
// message text, with only `data-compact` flipping between states.
//
// Two probes back the claims with numbers a screenshot can capture:
//   1. a static collapsed-vs-expanded pair that self-measures the footer offset
//      (footer-top delta should be 0px), and
//   2. an auto-toggling pair that counts how many times the footer subtree
//      MOUNTS. The stable structure mounts once and never again; an
//      old-style structure that re-parents the footer on toggle is included as
//      a contrast so the counter is demonstrably able to detect a remount.

import {
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
  type ReactNode,
  type Ref,
} from "react";
import { ArrowLeftIcon, CopyIcon } from "lucide-react";
import {
  BackLink,
  captionStyle,
  pageTitleStyle,
  sectionStyle,
  styleguideShellStyle,
} from "./shared";

const ONE_LINE_PROMPT =
  "this tiny little adjustment when I expand/collapse a one-line message is annoying";

// Counts its own mounts via a mount-only effect. Placed inside the footer so a
// footer remount is observable. Renders nothing.
function FooterMountProbe({ onMount }: { onMount: () => void }) {
  useEffect(() => {
    onMount();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);
  return null;
}

function Footer({
  footerRef,
  probe,
}: {
  footerRef?: Ref<HTMLDivElement>;
  probe?: () => void;
}) {
  return (
    <div ref={footerRef} className="run-msg-footer" data-always-visible>
      {probe ? <FooterMountProbe onMount={probe} /> : null}
      {/* TranscriptViewButton — the "open in transcript" affordance the user
          pointed at. An <a>, so it is the element the markdown-link rule would
          recolor blue if the footer were treated as prose. */}
      <a
        className="run-msg-action run-msg-transcript"
        href="#prompt-collapse-parity"
        title="Open message in transcript"
        aria-label="Open message in transcript"
        onClick={(e) => e.preventDefault()}
      >
        <ArrowLeftIcon size={12} aria-hidden="true" />
      </a>
      <button
        className="run-msg-action run-msg-copy"
        type="button"
        aria-label="Copy message"
      >
        <CopyIcon size={12} aria-hidden="true" />
      </button>
      <div className="run-msg-timings">
        <span className="run-msg-timing-row">7:48 PM</span>
      </div>
    </div>
  );
}

function ContextFrame({
  compact,
  children,
}: {
  compact: boolean;
  children: ReactNode;
}) {
  return (
    <div
      className="run-turn-view-context"
      aria-label="Turn prompt"
      data-collapsed={compact ? "true" : "false"}
      data-context-loaded="true"
      data-design-component="TurnPromptContext"
      data-design-state={compact ? "collapsed-one-line" : "expanded-one-line"}
      data-inspectable
    >
      <div
        className="run-transcript-message"
        data-slot="message"
        data-variant="user"
        data-role="user"
        data-kind="message"
        data-owner="activity"
        data-compact={compact ? "true" : undefined}
        data-inline-footer="true"
      >
        {children}
      </div>
    </div>
  );
}

// The shipped structure: one prompt-text element and one footer, both rendered
// unconditionally inside the message text. Only data-compact flips.
function StablePrompt({
  compact,
  contentRef,
  footerRef,
  probe,
}: {
  compact: boolean;
  contentRef?: Ref<HTMLDivElement>;
  footerRef?: Ref<HTMLDivElement>;
  probe?: () => void;
}) {
  return (
    <ContextFrame compact={compact}>
      <div
        ref={contentRef}
        className="run-transcript-message-content"
        data-slot="message-content"
      >
        <div
          className="run-transcript-message-text"
          data-slot="message-text"
          title={compact ? ONE_LINE_PROMPT : undefined}
        >
          <span className="run-plain-message-text">{ONE_LINE_PROMPT}</span>
          <Footer footerRef={footerRef} probe={probe} />
        </div>
      </div>
    </ContextFrame>
  );
}

// The pre-fix structure, kept ONLY as a contrast for the remount counter: the
// footer's JSX position depends on `compact` (sibling when collapsed, inside the
// text when expanded), so React re-parents — i.e. remounts — it on every toggle.
function RemountingPrompt({ compact, probe }: { compact: boolean; probe: () => void }) {
  const footer = <Footer probe={probe} />;
  return (
    <ContextFrame compact={compact}>
      <div className="run-transcript-message-content" data-slot="message-content">
        <div className="run-transcript-message-text" data-slot="message-text">
          {compact ? (
            <span className="run-msg-compact-text">{ONE_LINE_PROMPT}</span>
          ) : (
            <>
              <span className="run-plain-message-text">{ONE_LINE_PROMPT}</span>
              {footer}
            </>
          )}
        </div>
        {compact ? footer : null}
      </div>
    </ContextFrame>
  );
}

type Measurement = { footerTop: number; contentHeight: number };

function MeasuredBubble({
  compact,
  onMeasure,
}: {
  compact: boolean;
  onMeasure: (m: Measurement) => void;
}) {
  const contentRef = useRef<HTMLDivElement>(null);
  const footerRef = useRef<HTMLDivElement>(null);
  useLayoutEffect(() => {
    const measure = () => {
      const content = contentRef.current;
      const footer = footerRef.current;
      if (!content || !footer) return;
      const c = content.getBoundingClientRect();
      const f = footer.getBoundingClientRect();
      onMeasure({
        footerTop: Math.round((f.top - c.top) * 100) / 100,
        contentHeight: Math.round(c.height * 100) / 100,
      });
    };
    measure();
    const fonts = (document as unknown as { fonts?: { ready?: Promise<unknown> } })
      .fonts;
    fonts?.ready?.then(measure).catch(() => {});
    window.addEventListener("resize", measure);
    return () => window.removeEventListener("resize", measure);
  }, [compact, onMeasure]);
  return (
    <StablePrompt compact={compact} contentRef={contentRef} footerRef={footerRef} />
  );
}

const TOGGLE_TARGET = 6;

// Drives `compact` back and forth a fixed number of times and reports how many
// times the footer mounted. setInterval/useState are fine in the browser (only
// workflow scripts ban Date/timers).
function RemountProbe({
  structure,
}: {
  structure: "stable" | "remounting";
}) {
  const [compact, setCompact] = useState(true);
  const [toggles, setToggles] = useState(0);
  const mounts = useRef(0);
  const [mountCount, setMountCount] = useState(0);
  const bump = () => {
    mounts.current += 1;
    setMountCount(mounts.current);
  };
  useEffect(() => {
    let n = 0;
    const id = window.setInterval(() => {
      n += 1;
      setCompact((c) => !c);
      setToggles((t) => t + 1);
      if (n >= TOGGLE_TARGET) window.clearInterval(id);
    }, 350);
    return () => window.clearInterval(id);
  }, []);
  const ok = mountCount <= 1;
  return (
    <div style={{ display: "grid", gap: 8 }}>
      {structure === "stable" ? (
        <RemountProbeStable compact={compact} probe={bump} />
      ) : (
        <RemountingPrompt compact={compact} probe={bump} />
      )}
      <div
        data-design-element={`remount-${structure}`}
        style={{ fontSize: "var(--text-sm)", fontVariantNumeric: "tabular-nums" }}
      >
        <span style={{ color: "var(--text-faint)" }}>
          {structure === "stable" ? "stable DOM" : "pre-fix DOM (contrast)"} —{" "}
        </span>
        <span style={{ color: "var(--text-primary)" }}>
          footer mounts: {mountCount} across {toggles} toggles
        </span>{" "}
        <span style={{ fontWeight: 600, color: ok ? "#4ade80" : "#f87171" }}>
          {structure === "stable"
            ? ok
              ? "NO REMOUNT"
              : "REMOUNTED"
            : mountCount > 1
              ? "remounts each toggle (as expected)"
              : "—"}
        </span>
      </div>
    </div>
  );
}

function RemountProbeStable({
  compact,
  probe,
}: {
  compact: boolean;
  probe: () => void;
}) {
  return <StablePrompt compact={compact} probe={probe} />;
}

function ReadoutRow({ label, value }: { label: string; value: string }) {
  return (
    <div style={{ display: "flex", justifyContent: "space-between", gap: 16 }}>
      <span style={{ color: "var(--text-faint)" }}>{label}</span>
      <span
        style={{
          color: "var(--text-primary)",
          fontVariantNumeric: "tabular-nums",
        }}
      >
        {value}
      </span>
    </div>
  );
}

export function StyleguidePromptCollapseParity() {
  const [collapsed, setCollapsed] = useState<Measurement | null>(null);
  const [expanded, setExpanded] = useState<Measurement | null>(null);

  const footerDelta =
    collapsed && expanded
      ? Math.round(Math.abs(collapsed.footerTop - expanded.footerTop) * 100) / 100
      : null;
  const heightDelta =
    collapsed && expanded
      ? Math.round(
          Math.abs(collapsed.contentHeight - expanded.contentHeight) * 100,
        ) / 100
      : null;
  const stable =
    footerDelta != null &&
    heightDelta != null &&
    footerDelta <= 0.5 &&
    heightDelta <= 0.5;

  return (
    <div style={styleguideShellStyle}>
      <div style={{ maxWidth: 760 }}>
        <BackLink />
        <h1 style={pageTitleStyle}>prompt collapse / expand parity</h1>
        <p style={{ ...captionStyle, maxWidth: "70ch" }}>
          One-line Turns-view prompt rendered collapsed and expanded. Collapse is
          a pure CSS restyle of one stable DOM tree: the footer must not jolt,
          the left "open in transcript" arrow must stay muted, and the prompt +
          footer must not remount on toggle.
        </p>

        <section style={{ ...sectionStyle, display: "grid", gap: 20 }}>
          <div style={{ display: "grid", gap: 6 }}>
            <span style={captionStyle}>collapsed (compact one-line)</span>
            <MeasuredBubble compact onMeasure={setCollapsed} />
          </div>
          <div style={{ display: "grid", gap: 6 }}>
            <span style={captionStyle}>expanded (inline footer)</span>
            <MeasuredBubble compact={false} onMeasure={setExpanded} />
          </div>
        </section>

        <section
          style={{
            ...sectionStyle,
            display: "grid",
            gap: 8,
            padding: 16,
            border: "1px solid var(--border-soft)",
            borderRadius: "var(--radius-md)",
            background: "var(--bg-elevated)",
            fontSize: "var(--text-sm)",
          }}
          data-design-component="PromptCollapseParityReadout"
          data-inspectable
        >
          <ReadoutRow
            label="collapsed footer top / content height"
            value={
              collapsed
                ? `${collapsed.footerTop}px / ${collapsed.contentHeight}px`
                : "measuring…"
            }
          />
          <ReadoutRow
            label="expanded footer top / content height"
            value={
              expanded
                ? `${expanded.footerTop}px / ${expanded.contentHeight}px`
                : "measuring…"
            }
          />
          <ReadoutRow
            label="footer top Δ / content height Δ"
            value={
              footerDelta != null && heightDelta != null
                ? `${footerDelta}px / ${heightDelta}px`
                : "measuring…"
            }
          />
          <div
            data-design-element="parity-verdict"
            style={{
              marginTop: 6,
              fontWeight: 600,
              color: stable ? "#4ade80" : "#f87171",
            }}
          >
            {footerDelta == null
              ? "measuring…"
              : stable
                ? "STABLE — footer does not move on collapse/expand"
                : "JOLT — footer shifts between states"}
          </div>
        </section>

        <section style={{ ...sectionStyle, display: "grid", gap: 16 }}>
          <h2
            style={{
              margin: 0,
              color: "var(--text-primary)",
              fontSize: "var(--text-lg)",
            }}
          >
            remount probe (auto-toggling)
          </h2>
          <p style={{ ...captionStyle, maxWidth: "70ch" }}>
            Each card toggles itself collapsed/expanded {TOGGLE_TARGET} times and
            counts footer mounts. The shipped stable DOM mounts the footer once
            and never again; the pre-fix structure re-parents the footer every
            toggle, which is the remount that caused the flicker.
          </p>
          <RemountProbe structure="stable" />
          <RemountProbe structure="remounting" />
        </section>
      </div>
    </div>
  );
}
