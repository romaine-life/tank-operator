// Per-pod WebSocket fan-out for the codex SDK runtime. Sibling of
// agent-runner/src/ws.ts. Identical client-protocol surface so the
// orchestrator's /agent-ws proxy and the SPA's chat pane talk to either
// runner with the same frames.
//
// Frame shapes:
//   server → client: every SDK event, serialized as JSON.
//   client → server: { type: "user", message: { role: "user", content: ... } }
//                  | { type: "interrupt" }                         (abort turn)
//                  | { type: "heartbeat", last_order_key?: string } (heartbeat/ack)
//
// Auth happens upstream at the orchestrator's TLS+JWT termination; this
// server trusts whatever connects.

import { WebSocketServer, type WebSocket } from "ws";

export type ClientFrame =
  | { type: "user"; message: { role: "user"; content: unknown }; client_nonce?: string }
  | { type: "interrupt" }
  | { type: "heartbeat"; sent_at?: number; last_order_key?: string };

export class WSFanout {
  private readonly server: WebSocketServer;
  private readonly clients = new Set<WebSocket>();
  private onUserMessage: ((c: ClientFrame) => void) | null = null;

  constructor(port: number) {
    this.server = new WebSocketServer({ port, host: "0.0.0.0" });
    this.server.on("connection", (ws) => {
      this.clients.add(ws);
      ws.on("message", (raw) => {
        let frame: ClientFrame;
        try {
          frame = JSON.parse(raw.toString());
        } catch {
          return;
        }
        if (frame.type === "heartbeat") {
          ws.send(
            JSON.stringify({
              type: "heartbeat_ack",
              sent_at: frame.sent_at,
              last_order_key: frame.last_order_key,
              server_time: new Date().toISOString(),
            }),
          );
          return;
        }
        this.onUserMessage?.(frame);
      });
      ws.on("close", () => this.clients.delete(ws));
    });
  }

  onMessage(handler: (c: ClientFrame) => void): void {
    this.onUserMessage = handler;
  }

  // Broadcast a fully-serialized JSON line to all connected clients. Both
  // Cosmos and this fan-out see byte-identical payloads — the same producer
  // contract agent-runner follows.
  broadcast(serialized: string): void {
    for (const ws of this.clients) {
      if (ws.readyState === ws.OPEN) {
        ws.send(serialized);
      }
    }
  }

  // Broadcast a typed codex SDK event by serializing once. The event is
  // typed as `unknown` here because @openai/codex-sdk's ThreadEvent union
  // covers types we'll forward verbatim (the SPA renderer is the typed
  // consumer); keeping it opaque at this boundary keeps the dependency
  // surface small.
  broadcastEvent(event: unknown): void {
    this.broadcast(JSON.stringify(event));
  }

  close(): void {
    for (const ws of this.clients) {
      ws.close();
    }
    this.server.close();
  }
}
