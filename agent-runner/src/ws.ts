// Per-pod WebSocket fan-out. The orchestrator reverse-proxies the SPA's
// WebSocket onto this server (Phase C wiring). One pod has one runner has
// one WS server — multiple SPA clients (e.g., user has two tabs open)
// connect to the same instance and each gets the full event stream.
//
// Frame shapes:
//   server → client: every SDK event, serialized as JSON.
//   client → server: { type: "user", message: { role: "user", content: ... } }
//                  | { type: "interrupt" }                         (cancel turn)
//                  | { type: "ping" }                              (heartbeat)
//
// Authentication is the orchestrator's job (it terminates the user's TLS
// + JWT before proxying). This server trusts whatever connects.

import { WebSocketServer, type WebSocket } from "ws";
import type { SDKMessage } from "@anthropic-ai/claude-agent-sdk";

export type ClientFrame =
  | { type: "user"; message: { role: "user"; content: unknown } }
  | { type: "interrupt" }
  | { type: "ping" };

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
        if (frame.type === "ping") {
          ws.send(JSON.stringify({ type: "pong" }));
          return;
        }
        this.onUserMessage?.(frame);
      });
      ws.on("close", () => this.clients.delete(ws));
    });
  }

  onMessage(handler: (c: ClientFrame) => void) {
    this.onUserMessage = handler;
  }

  // Broadcast a fully-serialized JSON line to all connected clients. The
  // serialization happens once at the producer; both Cosmos and this fan-out
  // see byte-identical payloads (the producer contract).
  broadcast(serialized: string): void {
    for (const ws of this.clients) {
      if (ws.readyState === ws.OPEN) {
        ws.send(serialized);
      }
    }
  }

  // Broadcast a typed SDKMessage by serializing once.
  broadcastEvent(message: SDKMessage): void {
    this.broadcast(JSON.stringify(message));
  }

  close(): void {
    for (const ws of this.clients) {
      ws.close();
    }
    this.server.close();
  }
}
