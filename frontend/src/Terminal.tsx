import { forwardRef, useEffect, useImperativeHandle, useRef, useState } from "react";
import { Terminal as XTerm } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import { registerWrappedLinks } from "./wrappedLinkProvider";
import { ANSI_256_OVERRIDES, TERMINAL_THEME } from "./terminalTheme";
import { authedFetch } from "./auth";
import "@xterm/xterm/css/xterm.css";
import "./fonts.css";

function reportsAgentActivity(mode: string): boolean {
  return mode.startsWith("codex_") || mode.startsWith("pi_");
}

function usesCodexKeybindings(mode: string): boolean {
  return mode.startsWith("codex_");
}

function usesExtendedEnterKeybindings(mode: string): boolean {
  return mode.startsWith("pi_");
}

const CODEX_INSERT_NEWLINE = "\n";
const SHIFT_ENTER_CSI_U = "\x1b[13;2u";

const completionSound = (() => {
  let audio: HTMLAudioElement | null = null;
  let context: AudioContext | null = null;
  let unlocked = false;

  const getAudio = () => {
    audio ??= new Audio("/assets/upgrade-complete.mp3");
    audio.preload = "auto";
    audio.volume = 0.55;
    return audio;
  };

  const getContext = () => {
    context ??= new AudioContext();
    return context;
  };

  const unlock = () => {
    if (unlocked) return;
    getAudio().load();
    const ctx = getContext();
    void ctx.resume().then(() => {
      unlocked = true;
    }).catch(() => {
      // The browser may still require a later user gesture.
    });
  };

  const playFallback = (volume: number) => {
    const ctx = getContext();
    void ctx.resume().catch(() => undefined);

    const now = ctx.currentTime;
    const peak = Math.max(0.0001, Math.min(1, volume) * 0.145);
    const gain = ctx.createGain();
    const first = ctx.createOscillator();
    const second = ctx.createOscillator();

    first.type = "sine";
    first.frequency.setValueAtTime(880, now);
    second.type = "sine";
    second.frequency.setValueAtTime(1320, now);

    gain.gain.setValueAtTime(0.0001, now);
    gain.gain.exponentialRampToValueAtTime(peak, now + 0.015);
    gain.gain.exponentialRampToValueAtTime(0.0001, now + 0.22);

    first.connect(gain);
    second.connect(gain);
    gain.connect(ctx.destination);

    first.start(now);
    second.start(now + 0.04);
    first.stop(now + 0.22);
    second.stop(now + 0.22);
  };

  const play = (volume: number) => {
    const audio = getAudio();
    audio.volume = Math.max(0, Math.min(1, volume));
    audio.currentTime = 0;
    void audio.play().catch(() => playFallback(volume));
  };

  return { play, unlock };
})();

export type AgentActivity = "working" | "waiting";

interface Props {
  sessionId: string;
  mode: string;
  status: string;
  completionSoundEnabled: boolean;
  completionSoundVolume: number;
  onAgentActivityChange?: (sessionId: string, activity: AgentActivity) => void;
  /**
   * When false the component stays mounted (preserving WS + scrollback) but
   * the DOM is hidden via CSS. On every transition to true we re-run fit() so
   * xterm picks up the now-visible viewport size.
   */
  visible: boolean;
}

/**
 * Imperative handle the parent uses to push input into the live WS — used by
 * the inline "remote" affordance on the sidebar session row to send
 * "/remote-control\r" without the user having to focus the terminal and type.
 * No-op if the WS isn't open yet (the button only renders for Active sessions).
 */
export interface TerminalHandle {
  sendInput: (s: string) => void;
  focus: () => boolean;
}

type DebugWindow = Window & {
  __tankTerminalDebug?: Record<string, unknown>;
};

type XTermWithInternals = XTerm & {
  _core?: {
    _themeService?: {
      colors?: {
        ansi?: Array<{ css?: string; rgba?: number }>;
      };
    };
  };
};

function terminalDebugEnabled(flag?: string): boolean {
  try {
    const params = new URLSearchParams(window.location.search);
    if (flag && params.has(flag)) return true;
    if (params.has("terminalDebug")) return true;
    if (window.localStorage.getItem("tankTerminalDebug") === "1") return true;
    return flag ? window.localStorage.getItem(`tank${flag}`) === "1" : false;
  } catch {
    return false;
  }
}

function readXtermAnsiColor(term: XTerm, code: number): string | undefined {
  return (term as XTermWithInternals)._core?._themeService?.colors?.ansi?.[code]?.css;
}

function decodeTerminalPayload(data: string | ArrayBuffer): string {
  if (typeof data === "string") return data;
  return new TextDecoder().decode(new Uint8Array(data));
}

function ansiColorCodes(text: string): string[] {
  const codes = new Set<string>();
  const re = /\x1b\[([0-9;]*)m/g;
  let match: RegExpExecArray | null;
  while ((match = re.exec(text)) != null) {
    const parts = match[1].split(";");
    for (let i = 0; i < parts.length; i += 1) {
      if ((parts[i] === "38" || parts[i] === "48") && parts[i + 1] === "5" && parts[i + 2]) {
        codes.add(`${parts[i]};5;${parts[i + 2]}`);
        i += 2;
      }
    }
  }
  return [...codes];
}

function paletteProbeLine(): string {
  return [
    "\r\n[tank-terminal palette probe] ",
    "\x1b[38;5;174mfg174\x1b[39m ",
    "\x1b[38;5;211mfg211\x1b[39m ",
    "\x1b[38;5;220mfg220\x1b[39m ",
    "\x1b[38;5;153mfg153\x1b[39m\r\n",
  ].join("");
}

function isTextEntryElement(element: Element | null): boolean {
  if (!(element instanceof HTMLElement)) return false;
  const tagName = element.tagName.toLowerCase();
  return (
    element.isContentEditable ||
    tagName === "input" ||
    tagName === "textarea" ||
    tagName === "select" ||
    element.getAttribute("role") === "textbox"
  );
}

function focusTerminalIfSafe(term: XTerm): void {
  if (isTextEntryElement(document.activeElement)) return;
  term.focus();
}

function reportTerminalDebug(
  event: string,
  sessionId: string,
  mode: string,
  payload: Record<string, unknown>,
): void {
  const body = JSON.stringify({
    event,
    session_id: sessionId,
    mode,
    payload,
  });
  const blob = new Blob([body], { type: "application/json" });
  if (navigator.sendBeacon?.("/api/debug/terminal", blob)) return;
  void fetch("/api/debug/terminal", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body,
    keepalive: true,
  }).catch(() => undefined);
}

function imageFromClipboard(event: ClipboardEvent): File | null {
  const items = event.clipboardData?.items;
  if (items) {
    for (const item of Array.from(items)) {
      if (item.kind !== "file" || !item.type.startsWith("image/")) continue;
      const file = item.getAsFile();
      if (file) return file;
    }
  }
  const files = event.clipboardData?.files;
  if (files) {
    for (const file of Array.from(files)) {
      if (file.type.startsWith("image/")) return file;
    }
  }
  return null;
}

async function imageFromNavigatorClipboard(): Promise<Blob | null> {
  if (!navigator.clipboard?.read) return null;
  const items = await navigator.clipboard.read();
  for (const item of items) {
    const imageType = item.types.find((type) => type.startsWith("image/"));
    if (!imageType) continue;
    const blob = await item.getType(imageType);
    return blob;
  }
  return null;
}

export const Terminal = forwardRef<TerminalHandle, Props>(function Terminal(
  {
    sessionId,
    mode,
    status,
    visible,
    completionSoundEnabled,
    completionSoundVolume,
    onAgentActivityChange,
  },
  ref,
) {
  const containerRef = useRef<HTMLDivElement>(null);
  const fitRef = useRef<FitAddon | null>(null);
  const termRef = useRef<XTerm | null>(null);
  const wsRef = useRef<WebSocket | null>(null);
  const completionSoundEnabledRef = useRef(completionSoundEnabled);
  const completionSoundVolumeRef = useRef(completionSoundVolume);
  const onAgentActivityChangeRef = useRef(onAgentActivityChange);
  const [everActive, setEverActive] = useState(false);

  useEffect(() => {
    if (status === "Active") setEverActive(true);
  }, [status]);

  useEffect(() => {
    completionSoundEnabledRef.current = completionSoundEnabled;
  }, [completionSoundEnabled]);

  useEffect(() => {
    completionSoundVolumeRef.current = completionSoundVolume;
  }, [completionSoundVolume]);

  useEffect(() => {
    onAgentActivityChangeRef.current = onAgentActivityChange;
  }, [onAgentActivityChange]);

  useEffect(() => {
    window.addEventListener("pointerdown", completionSound.unlock, { once: true });
    window.addEventListener("keydown", completionSound.unlock, { once: true });
    return () => {
      window.removeEventListener("pointerdown", completionSound.unlock);
      window.removeEventListener("keydown", completionSound.unlock);
    };
  }, []);

  useImperativeHandle(ref, () => ({
    sendInput: (s: string) => {
      const ws = wsRef.current;
      if (ws && ws.readyState === WebSocket.OPEN) ws.send(s);
    },
    focus: () => {
      const term = termRef.current;
      if (!term) return false;
      fitRef.current?.fit();
      term.focus();
      return true;
    },
  }), []);

  useEffect(() => {
    if (!everActive) return;
    if (!containerRef.current) return;

    const term = new XTerm({
      cursorBlink: true,
      scrollback: 10_000,
      macOptionClickForcesSelection: true,
      // unicode-range in fonts.css keeps Nerd Font out of ASCII punctuation
      // while still letting it satisfy private-use terminal glyphs before the
      // browser settles for generic monospace tofu.
      fontFamily: 'ui-monospace, "Cascadia Code", "Consolas", "Symbols Nerd Font Mono", monospace',
      fontSize: 13,
      theme: TERMINAL_THEME,
    });
    const fit = new FitAddon();
    fitRef.current = fit;
    term.loadAddon(fit);
    const onBellDisp = term.onBell(() => {
      if (!reportsAgentActivity(mode)) return;
      onAgentActivityChangeRef.current?.(sessionId, "waiting");
      if (completionSoundEnabledRef.current) {
        completionSound.play(completionSoundVolumeRef.current);
      }
    });
    // URLs in claude's output become click-to-open. Custom provider (instead
    // of @xterm/addon-web-links) so links that wrap across terminal rows are
    // recognised as a single contiguous URL — the stock addon matches per
    // buffer line and would render a wrapped link as two broken halves.
    // Open in a new tab so the workspace session isn't navigated away from.
    registerWrappedLinks(term, (_event, uri) => {
      window.open(uri, "_blank", "noopener,noreferrer");
    });
    term.open(containerRef.current);
    termRef.current = term;
    const palette = Object.fromEntries(
      Object.keys(ANSI_256_OVERRIDES).map((code) => [
        code,
        {
          expected: ANSI_256_OVERRIDES[Number(code)],
          actual: readXtermAnsiColor(term, Number(code)),
        },
      ]),
    );
    reportTerminalDebug("palette-audit", sessionId, mode, { palette });
    if (terminalDebugEnabled()) {
      console.info("[tank-terminal] palette audit", {
        sessionId,
        mode,
        theme: TERMINAL_THEME,
        palette,
      });
      const debugWindow = window as DebugWindow;
      debugWindow.__tankTerminalDebug ??= {};
      debugWindow.__tankTerminalDebug[sessionId] = {
        term,
        palette: () => Object.fromEntries(
          [153, 174, 211, 220].map((code) => [code, readXtermAnsiColor(term, code)]),
        ),
        writePaletteProbe: () => term.write(paletteProbeLine()),
      };
    }
    if (terminalDebugEnabled("terminalPaletteProbe")) {
      term.write(paletteProbeLine());
    }
    const isDebugEnabled = () => {
      return terminalDebugEnabled();
    };
    const logTerminalEvent = (label: string, event: KeyboardEvent | WheelEvent, extra: Record<string, unknown> = {}) => {
      if (!isDebugEnabled()) return;
      const buffer = term.buffer.active;
      console.info("[tank-terminal]", label, {
        mode,
        baseY: buffer.baseY,
        viewportY: buffer.viewportY,
        cursorY: buffer.cursorY,
        activeType: buffer.type,
        defaultPrevented: event.defaultPrevented,
        ...extra,
      });
    };
    const canScrollXterm = (direction: -1 | 1) => {
      const buffer = term.buffer.active;
      return direction < 0 ? buffer.viewportY > 0 : buffer.viewportY < buffer.baseY;
    };
    const uploadPastedImage = async (image: Blob): Promise<void> => {
      term.write("\r\n\x1b[36m[uploading pasted image...]\x1b[0m\r\n");
      const response = await authedFetch(`/api/sessions/${sessionId}/paste-image`, {
        method: "POST",
        headers: { "Content-Type": image.type || "image/png" },
        body: image,
      });
      if (!response.ok) {
        throw new Error(`${response.status} ${await response.text()}`);
      }
      const { path } = await response.json() as { path: string };
      sendIfOpen(` ${path} `);
      term.write(`\x1b[36m[pasted image saved: ${path}]\x1b[0m\r\n`);
    };
    const pasteFromBrowserClipboard = async (): Promise<void> => {
      const image = await imageFromNavigatorClipboard();
      if (image) {
        await uploadPastedImage(image);
        return;
      }
      const text = await navigator.clipboard?.readText?.();
      if (text) term.paste(text);
    };
    const onCaptureKeyDown = (event: KeyboardEvent) => {
      if (event.key !== "PageUp" && event.key !== "PageDown") return;
      logTerminalEvent("capture keydown", event, {
        key: event.key,
        code: event.code,
        altKey: event.altKey,
        ctrlKey: event.ctrlKey,
        metaKey: event.metaKey,
        shiftKey: event.shiftKey,
        activeElement: document.activeElement?.tagName,
      });
    };
    containerRef.current.addEventListener("keydown", onCaptureKeyDown, { capture: true });
    term.attachCustomKeyEventHandler((event) => {
      if (
        event.type === "keydown"
        && event.key.toLowerCase() === "v"
        && (event.ctrlKey || event.metaKey)
        && !event.altKey
      ) {
        event.preventDefault();
        void pasteFromBrowserClipboard().catch((error) => {
          console.error("browser clipboard paste failed", error);
          term.write("\r\n\x1b[31m[failed to read browser clipboard]\x1b[0m\r\n");
        });
        return false;
      }
      if (event.key === "PageUp" || event.key === "PageDown") {
        logTerminalEvent("xterm custom key", event, {
          key: event.key,
          code: event.code,
          altKey: event.altKey,
          ctrlKey: event.ctrlKey,
          metaKey: event.metaKey,
          shiftKey: event.shiftKey,
        });
      }
      if (event.type !== "keydown") return true;
      if (event.altKey || event.ctrlKey || event.metaKey) return true;

      if (event.key === "Enter" && event.shiftKey && usesCodexKeybindings(mode)) {
        event.preventDefault();
        sendIfOpen(CODEX_INSERT_NEWLINE);
        return false;
      }

      if (event.key === "Enter" && event.shiftKey && usesExtendedEnterKeybindings(mode)) {
        event.preventDefault();
        sendIfOpen(SHIFT_ENTER_CSI_U);
        return false;
      }

      if (!mode.startsWith("codex_")) return true;

      if (event.key === "PageUp") {
        if (!canScrollXterm(-1)) {
          logTerminalEvent("xterm cannot scroll page", event, { direction: -1 });
          return true;
        }
        event.preventDefault();
        term.scrollPages(-1);
        logTerminalEvent("xterm scroll page", event, { direction: -1 });
        return false;
      }
      if (event.key === "PageDown") {
        if (!canScrollXterm(1)) {
          logTerminalEvent("xterm cannot scroll page", event, { direction: 1 });
          return true;
        }
        event.preventDefault();
        term.scrollPages(1);
        logTerminalEvent("xterm scroll page", event, { direction: 1 });
        return false;
      }

      return true;
    });
    const onWheel = (event: WheelEvent) => {
      const rawDirection = Math.sign(event.deltaY) as -1 | 0 | 1;
      logTerminalEvent("capture wheel", event, {
        deltaY: event.deltaY,
        ctrlKey: event.ctrlKey,
        direction: rawDirection,
      });
      if (!mode.startsWith("codex_") || event.ctrlKey || event.deltaY === 0) return;
      const direction = rawDirection as -1 | 1;
      if (!canScrollXterm(direction)) {
        logTerminalEvent("xterm cannot scroll wheel", event, { direction });
        return;
      }
      // Codex's TUI enables mouse reporting, which makes xterm forward wheel
      // events into the process instead of scrolling the browser terminal
      // viewport. Use xterm scrollback only when xterm actually has viewport
      // history to move through; otherwise let Codex's TUI handle the gesture.
      event.preventDefault();
      event.stopPropagation();
      term.scrollLines(direction * Math.max(1, Math.ceil(Math.abs(event.deltaY) / 30)));
      logTerminalEvent("wheel scroll lines", event, {
        direction,
      });
    };
    const onPaste = (event: ClipboardEvent) => {
      const image = imageFromClipboard(event);
      if (!image) return;
      event.preventDefault();
      event.stopPropagation();
      void uploadPastedImage(image).catch((error) => {
        console.error("image paste failed", error);
        term.write("\x1b[31m[failed to paste image]\x1b[0m\r\n");
      });
    };
    containerRef.current.addEventListener("wheel", onWheel, { capture: true, passive: false });
    containerRef.current.addEventListener("paste", onPaste, { capture: true });
    if (visible) {
      fit.fit();
      // Without this, the user has to click into the terminal before keystrokes
      // land — xterm doesn't auto-focus on mount.
      focusTerminalIfSafe(term);
    }

    const wsUrl = `${location.protocol === "https:" ? "wss:" : "ws:"}//${location.host}/api/sessions/${sessionId}/exec`;

    let cancelled = false;
    let reconnectTimer: number | null = null;
    let pingTimer: number | null = null;
    let backoffMs = 500;
    let everConnected = false;
    const maxBackoffMs = 15_000;

    const sendIfOpen = (payload: string | Uint8Array) => {
      const ws = wsRef.current;
      if (ws && ws.readyState === WebSocket.OPEN) ws.send(payload);
    };

    const sendResize = (cols: number, rows: number) => {
      sendIfOpen(JSON.stringify({ resize: [cols, rows] }));
    };

    const onWindowResize = () => {
      fit.fit();
      sendResize(term.cols, term.rows);
    };
    window.addEventListener("resize", onWindowResize);
    const onResizeDisp = term.onResize(({ cols, rows }) => sendResize(cols, rows));
    const onDataDisp = term.onData((data) => {
      if (reportsAgentActivity(mode) && (data.includes("\r") || data.includes("\n"))) {
        onAgentActivityChangeRef.current?.(sessionId, "working");
      }
      sendIfOpen(data);
    });

    const stopPing = () => {
      if (pingTimer != null) {
        window.clearInterval(pingTimer);
        pingTimer = null;
      }
    };

    const connect = () => {
      if (cancelled) return;
      const ws = new WebSocket(wsUrl);
      wsRef.current = ws;
      ws.binaryType = "arraybuffer";
      let debugPayloadLogs = 0;

      ws.onopen = () => {
        if (everConnected) {
          term.write("\r\n\x1b[32m[reconnected]\x1b[0m\r\n");
        }
        everConnected = true;
        backoffMs = 500;
        sendResize(term.cols, term.rows);
        // Heartbeat keeps Envoy's idle stream timeout (~5min default) from
        // cutting a quiet WS. Without it, the orchestrator's idle reaper
        // then deletes the pod 5min later — the session is gone for good.
        stopPing();
        pingTimer = window.setInterval(() => {
          sendIfOpen(JSON.stringify({ ping: 1 }));
        }, 30_000);
      };

      ws.onmessage = (e) => {
        if (terminalDebugEnabled() && debugPayloadLogs < 8) {
          const text = decodeTerminalPayload(e.data);
          const colors = ansiColorCodes(text);
          if (colors.length > 0) {
            debugPayloadLogs += 1;
            reportTerminalDebug("incoming-ansi-colors", sessionId, mode, { colors });
            console.info("[tank-terminal] incoming ansi colors", {
              sessionId,
              mode,
              colors,
              sample: text.slice(0, 500),
            });
          }
        }
        if (typeof e.data === "string") {
          term.write(e.data);
        } else {
          term.write(new Uint8Array(e.data as ArrayBuffer));
        }
      };

      ws.onclose = (e) => {
        stopPing();
        // code 1006 = abnormal closure with no close frame; for those the
        // browser drops `reason`. Show the code so failures are diagnosable.
        const detail = e.reason || `code ${e.code}`;
        // 1008 = policy violation (auth / not the session owner) — retrying
        // won't fix it. Everything else gets exponential backoff.
        if (cancelled || e.code === 1008) {
          term.write(`\r\n\x1b[33m[disconnected: ${detail}]\x1b[0m\r\n`);
          return;
        }
        const delay = backoffMs;
        term.write(
          `\r\n\x1b[33m[disconnected: ${detail}; reconnecting in ${Math.round(delay / 1000)}s…]\x1b[0m\r\n`,
        );
        backoffMs = Math.min(backoffMs * 2, maxBackoffMs);
        reconnectTimer = window.setTimeout(connect, delay);
      };

      ws.onerror = () => {
        // onclose fires next and drives the reconnect; nothing to do here.
      };
    };

    connect();

    return () => {
      cancelled = true;
      stopPing();
      if (reconnectTimer != null) window.clearTimeout(reconnectTimer);
      window.removeEventListener("resize", onWindowResize);
      containerRef.current?.removeEventListener("keydown", onCaptureKeyDown, { capture: true });
      containerRef.current?.removeEventListener("wheel", onWheel, { capture: true });
      containerRef.current?.removeEventListener("paste", onPaste, { capture: true });
      onResizeDisp.dispose();
      onDataDisp.dispose();
      onBellDisp.dispose();
      wsRef.current?.close();
      term.dispose();
      const debugWindow = window as DebugWindow;
      if (debugWindow.__tankTerminalDebug) {
        delete debugWindow.__tankTerminalDebug[sessionId];
      }
      fitRef.current = null;
      termRef.current = null;
      wsRef.current = null;
    };
    // visible intentionally omitted — we don't tear down on hide.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [sessionId, mode, everActive]);

  // Re-fit and re-focus whenever this tab becomes visible. xterm computes
  // rows/cols from the container's offsetWidth (0 while display:none), and
  // focus is lost when another tab steals it — refocusing on every show
  // means tabbing back lets the user type immediately.
  useEffect(() => {
    if (visible) {
      fitRef.current?.fit();
      const term = termRef.current;
      if (term) focusTerminalIfSafe(term);
    }
  }, [visible]);

  if (!everActive) {
    return (
      <div className="terminal-waiting" style={{ display: visible ? "flex" : "none" }}>
        waiting for pod to be ready… (status: {status})
      </div>
    );
  }
  return (
    <div
      ref={containerRef}
      className="terminal-body"
      style={{ display: visible ? "block" : "none" }}
    />
  );
});
