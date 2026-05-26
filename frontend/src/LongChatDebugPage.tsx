import { useCallback, useEffect, useRef, useState } from "react";
import type { CSSProperties } from "react";
import {
  ArrowDownIcon,
  ArrowUpIcon,
  Loader2Icon,
} from "lucide-react";
import { ChatComposer, type RunComposerMode } from "./ChatComposer";
import { WorkspaceShell } from "./WorkspaceShell";
import { getSessionAvatarByID } from "./sessionAvatars";
import {
  chatScrollElementSnapshot,
  logChatScrollEvent,
} from "./chatScrollTelemetry";
import { RunMessages, type TranscriptEntry } from "./App";
import { bootstrapAuth, startLogin } from "./auth";

const DEBUG_SESSION_ID = "debug-long-chat";
const INITIAL_TURN_COUNT = 240;
const PREPEND_TURN_COUNT = 80;
const TURN_TIME_BASE = Date.UTC(2026, 4, 21, 9, 0, 0);
const TURN_ORDER_OFFSET = 10_000;

const debugChatFontScaleStyle = {
  "--run-chat-font-scale": 1,
  "--run-chat-font-xs": "0.75rem",
  "--run-chat-font-sm": "0.875rem",
  "--run-chat-font-meta": "0.72rem",
  "--run-chat-font-code-xs": "0.7rem",
  "--run-chat-font-code-sm": "0.78rem",
  "--run-chat-font-star": "0.95rem",
} as CSSProperties;

export function LongChatDebugPage() {
  const [access, setAccess] = useState<"loading" | "admin" | "signed-out" | "denied">("loading");
  const [entries, setEntries] = useState<TranscriptEntry[]>(() =>
    makeMockTranscript(0, INITIAL_TURN_COUNT),
  );
  const [permissionMode, setPermissionMode] = useState<RunComposerMode>("default");
  const [scrollParent, setScrollParent] = useState<HTMLElement | null>(null);
  const [atBottom, setAtBottom] = useState(true);
  const [bottomDistance, setBottomDistance] = useState(0);
  const [running, setRunning] = useState(false);
  const [loadingOlder, setLoadingOlder] = useState(false);
  const [scrollToLatestSignal, setScrollToLatestSignal] = useState(0);
  const [scrollToOldestSignal, setScrollToOldestSignal] = useState(0);
  const oldestTurnRef = useRef(0);
  const newestTurnRef = useRef(INITIAL_TURN_COUNT - 1);
  const mockTimerRef = useRef<number | null>(null);
  const mockReplyIdRef = useRef<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    bootstrapAuth()
      .then((user) => {
        if (cancelled) return;
        if (!user) setAccess("signed-out");
        else setAccess(user.is_admin ? "admin" : "denied");
      })
      .catch(() => {
        if (!cancelled) setAccess("signed-out");
      });
    return () => {
      cancelled = true;
      if (mockTimerRef.current !== null) {
        window.clearInterval(mockTimerRef.current);
        mockTimerRef.current = null;
      }
    };
  }, []);

  useEffect(() => {
    if (!scrollParent) return;
    const update = () => {
      const distance = Math.max(
        0,
        scrollParent.scrollHeight - scrollParent.scrollTop - scrollParent.clientHeight,
      );
      setBottomDistance(Math.round(distance));
      setAtBottom(distance <= 4);
    };
    update();
    scrollParent.addEventListener("scroll", update, { passive: true });
    return () => scrollParent.removeEventListener("scroll", update);
  }, [scrollParent]);

  const bodyRef = useCallback((node: HTMLElement | null) => {
    setScrollParent(node);
    logChatScrollEvent(node ? "debug-scroll-parent-mounted" : "debug-scroll-parent-unmounted", {
      surface: "debug_lab",
      sessionMode: "debug",
      sessionId: DEBUG_SESSION_ID,
      ...chatScrollElementSnapshot(node),
    });
  }, []);

  const resetTranscript = useCallback((turnCount: number) => {
    oldestTurnRef.current = 0;
    newestTurnRef.current = turnCount - 1;
    setEntries(makeMockTranscript(0, turnCount));
    setAtBottom(true);
    setScrollToLatestSignal((value) => value + 1);
    logChatScrollEvent("debug-reset-transcript", {
      surface: "debug_lab",
      sessionMode: "debug",
      sessionId: DEBUG_SESSION_ID,
      turnCount,
      ...chatScrollElementSnapshot(scrollParent),
    });
  }, [scrollParent]);

  const prependOlder = useCallback(() => {
    if (loadingOlder) return;
    setLoadingOlder(true);
    const nextOldest = oldestTurnRef.current - PREPEND_TURN_COUNT;
    const older = makeMockTranscript(nextOldest, PREPEND_TURN_COUNT);
    window.setTimeout(() => {
      oldestTurnRef.current = nextOldest;
      setEntries((current) => [...older, ...current]);
      setLoadingOlder(false);
      logChatScrollEvent("debug-prepend-older", {
        surface: "debug_lab",
        sessionMode: "debug",
        sessionId: DEBUG_SESSION_ID,
        addedTurns: PREPEND_TURN_COUNT,
        oldestTurn: nextOldest,
        ...chatScrollElementSnapshot(scrollParent),
      });
    }, 250);
  }, [loadingOlder, scrollParent]);

  const appendBurst = useCallback((count = 12) => {
    const start = newestTurnRef.current + 1;
    newestTurnRef.current += count;
    const next = makeMockTranscript(start, count);
    setEntries((current) => [...current, ...next]);
    logChatScrollEvent("debug-append-burst", {
      surface: "debug_lab",
      sessionMode: "debug",
      sessionId: DEBUG_SESSION_ID,
      addedTurns: count,
      atBottom,
      ...chatScrollElementSnapshot(scrollParent),
    });
  }, [atBottom, scrollParent]);

  const stopMockReply = useCallback(() => {
    if (mockTimerRef.current !== null) {
      window.clearInterval(mockTimerRef.current);
      mockTimerRef.current = null;
    }
    const stoppedId = mockReplyIdRef.current;
    mockReplyIdRef.current = null;
    setRunning(false);
    if (stoppedId) {
      setEntries((current) =>
        current.map((entry) =>
          entry.id === stoppedId && entry.kind === "message"
            ? { ...entry, text: `${entry.text}\n\n[Mock reply stopped]` }
            : entry,
        ),
      );
    }
    logChatScrollEvent("debug-mock-reply-stopped", {
      surface: "debug_lab",
      sessionMode: "debug",
      sessionId: DEBUG_SESSION_ID,
      replyId: stoppedId ?? "",
      ...chatScrollElementSnapshot(scrollParent),
    });
  }, [scrollParent]);

  const submitMockMessage = useCallback(({ text }: { text: string }) => {
    const trimmed = text.trim();
    if (!trimmed || running) return;
    const turn = newestTurnRef.current + 1;
    newestTurnRef.current = turn;
    const userEntry = mockMessageEntry(turn, 0, "user", trimmed, "posted");
    const replyId = mockId("assistant", turn, "posted");
    const replyChunks = mockReplyFor(trimmed, turn).split(" ");
    mockReplyIdRef.current = replyId;
    setRunning(true);
    setEntries((current) => [
      ...current,
      userEntry,
      mockMessageEntry(turn, 3, "assistant", "Thinking...", "posted", replyId),
    ]);
    logChatScrollEvent("debug-submit-message", {
      surface: "debug_lab",
      sessionMode: "debug",
      sessionId: DEBUG_SESSION_ID,
      turn,
      atBottom,
      ...chatScrollElementSnapshot(scrollParent),
    });
    let chunkIndex = 0;
    mockTimerRef.current = window.setInterval(() => {
      chunkIndex += 4;
      const nextText = replyChunks.slice(0, chunkIndex).join(" ");
      setEntries((current) =>
        current.map((entry) =>
          entry.id === replyId && entry.kind === "message"
            ? { ...entry, text: nextText || "Thinking..." }
            : entry,
        ),
      );
      if (chunkIndex >= replyChunks.length) {
        if (mockTimerRef.current !== null) {
          window.clearInterval(mockTimerRef.current);
          mockTimerRef.current = null;
        }
        mockReplyIdRef.current = null;
        setRunning(false);
        logChatScrollEvent("debug-mock-reply-complete", {
          surface: "debug_lab",
          sessionMode: "debug",
          sessionId: DEBUG_SESSION_ID,
          turn,
          ...chatScrollElementSnapshot(scrollParent),
        });
      }
    }, 120);
  }, [atBottom, running, scrollParent]);

  const avatar = getSessionAvatarByID("jp1-malcolm");

  if (access === "loading") {
    return <DebugAccessScreen title="Loading debug session..." />;
  }
  if (access === "signed-out") {
    return (
      <DebugAccessScreen
        title="Sign in required"
        actionLabel="Sign in"
        onAction={() => void startLogin()}
      />
    );
  }
  if (access !== "admin") {
    return <DebugAccessScreen title="Admin access required" />;
  }

  return (
    <div className="debug-scroll-lab">
      <WorkspaceShell
        className="debug-long-chat"
        style={debugChatFontScaleStyle}
        bodyRef={bodyRef}
        bodyClassName={running ? "run-main-running" : "run-main-idle"}
        title={(
          <div className="debug-long-chat-title">
            <span>Long chat scroll lab</span>
            <small>
              {entries.length} entries - {bottomDistance <= 4 ? "at tail" : `${bottomDistance}px above tail`}
            </small>
          </div>
        )}
        tabs={(
          <>
            <button type="button" className="run-tab" onClick={() => resetTranscript(240)}>
              240 turns
            </button>
            <button type="button" className="run-tab" onClick={() => resetTranscript(900)}>
              900 turns
            </button>
            <button type="button" className="run-tab" onClick={prependOlder}>
              {loadingOlder ? <Loader2Icon className="run-spin" size={14} /> : "Prepend"}
            </button>
            <button type="button" className="run-tab" onClick={() => appendBurst(12)}>
              Burst
            </button>
          </>
        )}
        body={(
          <>
            {loadingOlder ? (
              <div className="run-transcript-load-older run-transcript-load-older-passive">
                Loading earlier messages...
              </div>
            ) : (
              <button type="button" className="run-transcript-load-older" onClick={prependOlder}>
                Load earlier messages
              </button>
            )}
            <RunMessages
              entries={entries}
              avatar={avatar}
              sessionId={DEBUG_SESSION_ID}
              sessionMode="debug"
              telemetrySurface="debug_lab"
              showThinking
              autoExpandTools={false}
              showTimestamps
              showDuration={false}
              onQuote={() => undefined}
              scrollParent={scrollParent}
              onStartReached={prependOlder}
              onAtBottomChange={setAtBottom}
              scrollToLatestSignal={scrollToLatestSignal}
              scrollToOldestSignal={scrollToOldestSignal}
            />
          </>
        )}
        floatingBetweenBodyAndComposer={(
          <>
            {!atBottom && (
              <button
                type="button"
                className="run-scroll-to-top"
                onClick={() => setScrollToOldestSignal((value) => value + 1)}
                aria-label="Scroll to beginning of mock conversation"
              >
                <ArrowUpIcon size={16} strokeWidth={2.2} aria-hidden="true" />
              </button>
            )}
            <button
              type="button"
              className={`run-scroll-to-bottom${atBottom ? " run-scroll-to-bottom-hidden" : ""}`}
              onClick={() => setScrollToLatestSignal((value) => value + 1)}
              aria-label="Scroll to latest mock message"
            >
              <ArrowDownIcon size={16} strokeWidth={2.2} aria-hidden="true" />
            </button>
          </>
        )}
        composer={(
          <ChatComposer
            placeholder="Send a mock message"
            onSubmit={submitMockMessage}
            permissionMode={permissionMode}
            onPermissionModeChange={setPermissionMode}
            sendByCtrlEnter={false}
            submitStatus={running ? "streaming" : undefined}
            onStop={stopMockReply}
            isStopping={false}
          />
        )}
      />
    </div>
  );
}

function DebugAccessScreen({
  title,
  actionLabel,
  onAction,
}: {
  title: string;
  actionLabel?: string;
  onAction?: () => void;
}) {
  return (
    <div className="debug-scroll-access">
      <div>
        <h1>{title}</h1>
        {actionLabel && onAction && (
          <button type="button" className="btn-primary" onClick={onAction}>
            {actionLabel}
          </button>
        )}
      </div>
    </div>
  );
}

function makeMockTranscript(startTurn: number, count: number): TranscriptEntry[] {
  const entries: TranscriptEntry[] = [];
  for (let turn = startTurn; turn < startTurn + count; turn += 1) {
    entries.push(...makeMockTurn(turn));
  }
  return entries;
}

function makeMockTurn(turn: number): TranscriptEntry[] {
  const entries: TranscriptEntry[] = [
    mockMessageEntry(
      turn,
      0,
      "user",
      `Mock prompt ${turn}: check scroll behavior around ${turn % 2 === 0 ? "streaming output" : "older history"}.`,
    ),
  ];
  if (turn % 9 === 0) {
    entries.push({
      id: mockId("reasoning", turn),
      kind: "reasoning",
      reasoning: {
        text: `Reasoning trace for turn ${turn}. The block is intentionally short but present so virtual row heights vary.`,
      },
      time: mockTime(turn, 1),
      orderKey: mockOrderKey(turn, 1),
    } as TranscriptEntry);
  }
  if (turn % 6 === 0) {
    entries.push(mockToolEntry(turn, 2));
    if (turn % 12 === 0) entries.push(mockToolEntry(turn, 3, "Read", "completed"));
  }
  entries.push(
    mockMessageEntry(turn, 4, "assistant", mockAssistantText(turn)),
  );
  return entries;
}

function mockMessageEntry(
  turn: number,
  sequence: number,
  role: "user" | "assistant" | "system",
  text: string,
  prefix = "seed",
  id = mockId(role, turn, prefix),
): TranscriptEntry {
  return {
    id,
    kind: "message",
    role,
    text,
    time: mockTime(turn, sequence),
    orderKey: mockOrderKey(turn, sequence),
  } as TranscriptEntry;
}

function mockToolEntry(
  turn: number,
  sequence: number,
  toolName = "Bash",
  status = turn % 18 === 0 ? "failed" : "completed",
): TranscriptEntry {
  return {
    id: mockId(`tool-${sequence}`, turn),
    kind: "tool",
    toolName,
    toolKind: toolName === "Bash" ? "shell" : "mcp",
    toolInput: toolName === "Bash"
      ? `printf "turn ${turn}\\n"`
      : JSON.stringify({ file_path: `/workspace/mock-${turn}.ts` }, null, 2),
    toolOutput: status === "failed"
      ? "exit status 1: mock failure used to test expanded tool heights"
      : `mock output for turn ${turn}`,
    toolStatus: status,
    time: mockTime(turn, sequence),
    startedAt: mockTime(turn, sequence),
    completedAt: mockTime(turn, sequence + 0.5),
    orderKey: mockOrderKey(turn, sequence),
  } as TranscriptEntry;
}

function mockAssistantText(turn: number): string {
  const paragraphCount = 1 + Math.abs(turn % 4);
  const paragraphs = Array.from({ length: paragraphCount }, (_, index) =>
    `Assistant reply ${turn}.${index + 1}: ${mockSentence(turn, index)} The varying length is deliberate so row measurement changes while scrolling.`,
  );
  if (turn % 8 === 0) {
    paragraphs.push(
      [
        "```ts",
        `const turn = ${turn};`,
        "const stable = turn % 2 === 0;",
        "console.log({ turn, stable });",
        "```",
      ].join("\n"),
    );
  }
  if (turn % 10 === 0) {
    paragraphs.push("- first mock checklist item\n- second mock checklist item\n- third mock checklist item");
  }
  return paragraphs.join("\n\n");
}

function mockSentence(turn: number, index: number): string {
  const bank = [
    "This branch adds enough content to force a tall markdown bubble.",
    "The next event should not pull the viewport unless the tail is pinned.",
    "Back-pagination should preserve the first visible item after prepend.",
    "The bottom affordance should show up only after the tail is no longer visible.",
  ];
  return bank[Math.abs(turn + index) % bank.length]!;
}

function mockReplyFor(prompt: string, turn: number): string {
  return [
    `Mock response for turn ${turn}.`,
    `You posted: "${prompt.slice(0, 120)}".`,
    "This reply streams in chunks so follow-output, tail pinning, and Prometheus scroll metrics can be inspected without a live runner.",
    "If you scroll upward before it finishes, the viewport should stay where you left it and the latest button should become reachable.",
  ].join(" ");
}

function mockId(kind: string, turn: number, prefix = "seed"): string {
  return `debug-${prefix}-${kind}-${turn < 0 ? `neg${Math.abs(turn)}` : turn}`;
}

function mockTime(turn: number, sequence: number): string {
  return new Date(TURN_TIME_BASE + (turn + TURN_ORDER_OFFSET) * 60_000 + sequence * 1000).toISOString();
}

function mockOrderKey(turn: number, sequence: number): string {
  const millis = TURN_TIME_BASE + (turn + TURN_ORDER_OFFSET) * 60_000 + sequence * 1000;
  return `${String(millis).padStart(13, "0")}-${String(Math.round(sequence * 1000)).padStart(8, "0")}-debug`;
}
