// SDK-aware run pane (Phase D). Lives next to the legacy HeadlessRun
// during the rollout; chosen at render time when session.runtime === "sdk".
//
// Contract:
//   - GET /api/sessions/{id}/events?after=<uuid>  → history backfill
//   - WS  /api/sessions/{id}/agent-ws             → live tap
//   - Server emits canonical SDK events; we dedupe by `uuid` so the join
//     between history-replay and live is idempotent.
//   - Client → server frames are { type:"user", message:{...} } |
//     { type:"interrupt" }. Slash commands are sent as regular user
//     messages — the SDK / claude binary handles them.
//
// Renderer reuses the project's prose styles via className strings; it
// does not import the legacy TranscriptEntry shape. SDK message types
// are rendered directly so Phase F can delete HeadlessRun without
// disturbing this file.

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { KeyboardEvent as ReactKeyboardEvent } from "react";

import { authedFetch, getStoredToken } from "./auth";

// Minimal local typing for SDK messages. We intentionally don't pull
// `@anthropic-ai/claude-agent-sdk` into the frontend bundle — its
// transitive deps assume a Node runtime. The wire format is JSON, so
// `unknown`-ish typing with discriminator narrowing is fine here.
type ContentBlock =
  | { type: "text"; text: string }
  | { type: "thinking"; thinking: string }
  | { type: "tool_use"; id: string; name: string; input: unknown }
  | { type: "tool_result"; tool_use_id: string; content: unknown; is_error?: boolean }
  | { type: "image"; source: { type: string; media_type?: string; data?: string; url?: string } }
  | { type: string; [k: string]: unknown };

interface AssistantMessage {
  type: "assistant";
  uuid: string;
  message: { role: "assistant"; content: ContentBlock[]; model?: string };
  parent_tool_use_id?: string | null;
  session_id?: string;
}

interface UserMessage {
  type: "user";
  uuid: string;
  message: { role: "user"; content: ContentBlock[] | string };
  parent_tool_use_id?: string | null;
  session_id?: string;
}

interface SystemMessage {
  type: "system";
  uuid: string;
  subtype?: string;
  // init carries cwd / model / tools / mcp_servers; other subtypes vary.
  [k: string]: unknown;
}

interface ResultMessage {
  type: "result";
  uuid: string;
  subtype?: string;
  duration_ms?: number;
  duration_api_ms?: number;
  is_error?: boolean;
  num_turns?: number;
  total_cost_usd?: number;
  usage?: { input_tokens?: number; output_tokens?: number; cache_read_input_tokens?: number };
}

interface RateLimitMessage {
  type: "rate_limit";
  uuid: string;
  message?: string;
  retry_after_ms?: number;
}

interface GenericCanonical {
  type: string;
  uuid: string;
  [k: string]: unknown;
}

type SDKEvent =
  | AssistantMessage
  | UserMessage
  | SystemMessage
  | ResultMessage
  | RateLimitMessage
  | GenericCanonical;

// Live-only stream delta. Not durable; we use it for the typewriter
// effect under the in-progress message. See agent-runner/src/cosmos.ts
// for the canonical/live split.
interface StreamEvent {
  type: "stream_event";
  uuid?: string;
  // The SDK wraps the upstream Anthropic API event under `event`.
  event?: {
    type: string;
    delta?: { type?: string; text?: string };
    content_block?: { type?: string };
  };
}

interface RunPaneSDKProps {
  sessionId: string;
  visible: boolean;
  onActivityChange?: (id: string, activity: "waiting" | "working") => void;
}

export function RunPaneSDK({ sessionId, visible, onActivityChange }: RunPaneSDKProps) {
  const [events, setEvents] = useState<SDKEvent[]>([]);
  const [partialText, setPartialText] = useState("");
  const [connected, setConnected] = useState(false);
  const [running, setRunning] = useState(false);
  const [composer, setComposer] = useState("");
  const [queued, setQueued] = useState<string[]>([]);
  const [error, setError] = useState<string | null>(null);

  // uuid → seen, so the history-replay + live join is idempotent. Lives
  // outside React state so neither the history fetch nor the WS callback
  // need to read the latest events[] to know what's already there.
  const seenRef = useRef<Set<string>>(new Set());
  const wsRef = useRef<WebSocket | null>(null);
  const scrollerRef = useRef<HTMLDivElement | null>(null);

  const appendEvent = useCallback((ev: SDKEvent) => {
    const id = ev.uuid;
    if (!id) return;
    if (seenRef.current.has(id)) return;
    seenRef.current.add(id);
    setEvents((prev) => [...prev, ev]);
  }, []);

  // History backfill. Runs once per session change. We pass after="" to
  // get everything; the server returns events strictly after that
  // watermark in ascending uuid order.
  useEffect(() => {
    let cancelled = false;
    seenRef.current = new Set();
    setEvents([]);
    setPartialText("");
    setError(null);
    (async () => {
      try {
        const res = await authedFetch(`/api/sessions/${sessionId}/events?limit=1000`);
        if (!res.ok) throw new Error(`history fetch ${res.status}`);
        const body = (await res.json()) as { events: SDKEvent[] };
        if (cancelled) return;
        for (const ev of body.events ?? []) {
          appendEvent(ev);
        }
      } catch (e) {
        if (!cancelled) setError(`history: ${(e as Error).message}`);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [sessionId, appendEvent]);

  // Live WebSocket. Browsers can't set Authorization on WS upgrades, so
  // the orchestrator accepts the JWT via cookie (set at login). The
  // protocol is text JSON frames; no subprotocol negotiation.
  useEffect(() => {
    const scheme = window.location.protocol === "https:" ? "wss:" : "ws:";
    // Token in query string is the desktop-app fallback when cookies
    // don't ride along (electron). Web path uses the cookie.
    const token = getStoredToken();
    const tokenParam = token ? `?auth=${encodeURIComponent(token)}` : "";
    const url = `${scheme}//${window.location.host}/api/sessions/${sessionId}/agent-ws${tokenParam}`;
    const ws = new WebSocket(url);
    wsRef.current = ws;
    let openedAt = 0;

    ws.onopen = () => {
      openedAt = Date.now();
      setConnected(true);
      setError(null);
    };
    ws.onclose = (e) => {
      setConnected(false);
      wsRef.current = null;
      // Only surface a closed-too-fast error if the open never settled or
      // closed within a second — otherwise the close is the user navigating
      // away and there's no point yelling about it.
      if (Date.now() - openedAt < 1000) {
        setError(`agent-ws closed (${e.code})`);
      }
    };
    ws.onerror = () => {
      // The browser fires error before close; close handler does the work.
    };
    ws.onmessage = (msg) => {
      let parsed: unknown;
      try {
        parsed = JSON.parse(typeof msg.data === "string" ? msg.data : "");
      } catch {
        return;
      }
      if (!parsed || typeof parsed !== "object") return;
      const ev = parsed as SDKEvent | StreamEvent;
      if (ev.type === "stream_event") {
        const delta = (ev as StreamEvent).event?.delta;
        if (delta?.type === "text_delta" && typeof delta.text === "string") {
          setPartialText((prev) => prev + delta.text);
        }
        return;
      }
      // Any non-stream event tears down the partial buffer. If it's an
      // assistant message, the durable text replaces what the partial
      // was showing. If it's a result, the turn is over.
      if (ev.type === "assistant") {
        setPartialText("");
      }
      if (ev.type === "result") {
        setPartialText("");
        setRunning(false);
        // Flush the next queued message, if any.
        setQueued((prev) => {
          if (prev.length === 0) return prev;
          const [next, ...rest] = prev;
          sendUserMessage(next);
          return rest;
        });
      }
      if (ev.type === "user") {
        // A user event from the wire means a turn is about to run.
        setRunning(true);
      }
      appendEvent(ev as SDKEvent);
    };

    return () => {
      try {
        ws.close();
      } catch {
        // ignore
      }
      wsRef.current = null;
    };
    // Send helper is hoisted below; the dep on appendEvent is stable.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [sessionId, appendEvent]);

  useEffect(() => {
    onActivityChange?.(sessionId, running ? "working" : "waiting");
  }, [onActivityChange, running, sessionId]);

  // Auto-scroll on new events when the user is already at the bottom.
  useEffect(() => {
    const el = scrollerRef.current;
    if (!el) return;
    const near = el.scrollHeight - el.scrollTop - el.clientHeight < 120;
    if (near) {
      el.scrollTop = el.scrollHeight;
    }
  }, [events.length, partialText]);

  const sendUserMessage = useCallback((text: string) => {
    const ws = wsRef.current;
    if (!ws || ws.readyState !== WebSocket.OPEN) {
      setError("agent-ws not connected");
      return;
    }
    const frame = {
      type: "user" as const,
      message: { role: "user" as const, content: text },
    };
    ws.send(JSON.stringify(frame));
    setRunning(true);
  }, []);

  const handleSubmit = useCallback(() => {
    const text = composer.trim();
    if (!text) return;
    setComposer("");
    if (running) {
      // Codex-style queueing. Enter on a busy session queues; the next
      // result event flushes the head of the queue.
      setQueued((prev) => [...prev, text]);
      return;
    }
    sendUserMessage(text);
  }, [composer, running, sendUserMessage]);

  const handleInterrupt = useCallback(() => {
    const ws = wsRef.current;
    if (!ws || ws.readyState !== WebSocket.OPEN) return;
    ws.send(JSON.stringify({ type: "interrupt" }));
  }, []);

  const handleKey = useCallback(
    (e: ReactKeyboardEvent<HTMLTextAreaElement>) => {
      if (e.key === "Enter" && !e.shiftKey) {
        e.preventDefault();
        handleSubmit();
      }
    },
    [handleSubmit],
  );

  const rendered = useMemo(() => events.map((ev) => renderEvent(ev)), [events]);

  if (!visible) return null;

  return (
    <div className="sdk-run-pane">
      <div className="sdk-run-status">
        <span className={`sdk-run-dot ${connected ? "ok" : "off"}`} />
        <span>{connected ? "connected" : "connecting…"}</span>
        {running && <span className="sdk-run-running">working…</span>}
        {error && <span className="sdk-run-error">{error}</span>}
      </div>
      <div ref={scrollerRef} className="sdk-run-transcript">
        {rendered}
        {partialText && (
          <div className="sdk-event sdk-event-assistant sdk-event-partial">
            <pre>{partialText}</pre>
          </div>
        )}
        {queued.length > 0 && (
          <div className="sdk-run-queued">
            {queued.length} message{queued.length === 1 ? "" : "s"} queued
          </div>
        )}
      </div>
      <div className="sdk-run-composer">
        <textarea
          value={composer}
          onChange={(e) => setComposer(e.target.value)}
          onKeyDown={handleKey}
          placeholder={running ? "queue a follow-up… (Enter)" : "send a message… (Enter)"}
          rows={3}
        />
        <div className="sdk-run-composer-bar">
          {running ? (
            <button type="button" onClick={handleInterrupt}>
              interrupt
            </button>
          ) : (
            <button type="button" onClick={handleSubmit} disabled={!composer.trim()}>
              send
            </button>
          )}
        </div>
      </div>
    </div>
  );
}

function renderEvent(ev: SDKEvent) {
  const key = ev.uuid;
  switch (ev.type) {
    case "system":
      return renderSystem(ev as SystemMessage, key);
    case "assistant":
      return renderAssistant(ev as AssistantMessage, key);
    case "user":
      return renderUser(ev as UserMessage, key);
    case "result":
      return renderResult(ev as ResultMessage, key);
    case "rate_limit":
      return renderRateLimit(ev as RateLimitMessage, key);
    default:
      return (
        <div key={key} className="sdk-event sdk-event-meta">
          <span className="sdk-event-label">{ev.type}</span>
        </div>
      );
  }
}

function renderSystem(ev: SystemMessage, key: string) {
  const subtype = ev.subtype ?? "system";
  if (subtype === "init") {
    const model = (ev as any).model as string | undefined;
    const cwd = (ev as any).cwd as string | undefined;
    return (
      <div key={key} className="sdk-event sdk-event-meta">
        <span className="sdk-event-label">session started</span>
        {model && <span className="sdk-event-tag">{model}</span>}
        {cwd && <span className="sdk-event-tag">{cwd}</span>}
      </div>
    );
  }
  if (subtype === "compact_boundary") {
    return (
      <div key={key} className="sdk-event sdk-event-divider">
        <span>context compacted</span>
      </div>
    );
  }
  return (
    <div key={key} className="sdk-event sdk-event-meta">
      <span className="sdk-event-label">{subtype}</span>
    </div>
  );
}

function renderAssistant(ev: AssistantMessage, key: string) {
  const blocks = ev.message?.content ?? [];
  return (
    <div key={key} className="sdk-event sdk-event-assistant">
      {blocks.map((b, i) => renderBlock(b, `${key}:${i}`, "assistant"))}
    </div>
  );
}

function renderUser(ev: UserMessage, key: string) {
  const content = ev.message?.content;
  // The pod-side runner sends user text as a string; the SDK normalizes
  // to an array of blocks. Handle both.
  if (typeof content === "string") {
    return (
      <div key={key} className="sdk-event sdk-event-user">
        <pre>{content}</pre>
      </div>
    );
  }
  const blocks = content ?? [];
  return (
    <div key={key} className="sdk-event sdk-event-user">
      {blocks.map((b, i) => renderBlock(b, `${key}:${i}`, "user"))}
    </div>
  );
}

function renderBlock(block: ContentBlock, key: string, _role: "assistant" | "user") {
  if (block.type === "text") {
    return (
      <pre key={key} className="sdk-block sdk-block-text">
        {(block as { text: string }).text}
      </pre>
    );
  }
  if (block.type === "thinking") {
    return (
      <details key={key} className="sdk-block sdk-block-thinking">
        <summary>thinking</summary>
        <pre>{(block as { thinking: string }).thinking}</pre>
      </details>
    );
  }
  if (block.type === "tool_use") {
    const tu = block as { name: string; input: unknown };
    return (
      <div key={key} className="sdk-block sdk-block-tool-use">
        <span className="sdk-block-label">{tu.name}</span>
        <pre>{safeStringify(tu.input)}</pre>
      </div>
    );
  }
  if (block.type === "tool_result") {
    const tr = block as { content: unknown; is_error?: boolean };
    return (
      <div
        key={key}
        className={`sdk-block sdk-block-tool-result ${tr.is_error ? "is-error" : ""}`}
      >
        <span className="sdk-block-label">{tr.is_error ? "tool error" : "tool result"}</span>
        <pre>{renderToolResultContent(tr.content)}</pre>
      </div>
    );
  }
  if (block.type === "image") {
    const img = block as { source: { type: string; media_type?: string; data?: string; url?: string } };
    if (img.source.type === "base64" && img.source.data && img.source.media_type) {
      return (
        <img
          key={key}
          className="sdk-block sdk-block-image"
          src={`data:${img.source.media_type};base64,${img.source.data}`}
          alt=""
        />
      );
    }
    if (img.source.type === "url" && img.source.url) {
      return <img key={key} className="sdk-block sdk-block-image" src={img.source.url} alt="" />;
    }
    return (
      <div key={key} className="sdk-block sdk-block-meta">
        image ({img.source.type})
      </div>
    );
  }
  return (
    <div key={key} className="sdk-block sdk-block-meta">
      {block.type}
    </div>
  );
}

function renderResult(ev: ResultMessage, key: string) {
  const ms = ev.duration_ms;
  const seconds = ms ? (ms / 1000).toFixed(1) : null;
  const cost = ev.total_cost_usd;
  return (
    <div key={key} className="sdk-event sdk-event-result">
      <span className="sdk-event-label">turn complete</span>
      {seconds && <span className="sdk-event-tag">{seconds}s</span>}
      {typeof cost === "number" && <span className="sdk-event-tag">${cost.toFixed(4)}</span>}
      {ev.usage?.output_tokens && (
        <span className="sdk-event-tag">{ev.usage.output_tokens} out</span>
      )}
    </div>
  );
}

function renderRateLimit(ev: RateLimitMessage, key: string) {
  return (
    <div key={key} className="sdk-event sdk-event-rate-limit">
      <span className="sdk-event-label">rate limit</span>
      <span>{ev.message ?? "throttled"}</span>
    </div>
  );
}

function renderToolResultContent(content: unknown): string {
  if (typeof content === "string") return content;
  if (Array.isArray(content)) {
    return content
      .map((c) => {
        if (c && typeof c === "object" && (c as { type?: string }).type === "text") {
          return (c as { text: string }).text;
        }
        return safeStringify(c);
      })
      .join("\n");
  }
  return safeStringify(content);
}

function safeStringify(v: unknown): string {
  try {
    return JSON.stringify(v, null, 2);
  } catch {
    return String(v);
  }
}
