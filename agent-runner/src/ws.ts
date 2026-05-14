// Per-pod WebSocket fan-out. The orchestrator reverse-proxies the SPA's
// WebSocket onto this server. One pod has one runner has
// one WS server — multiple SPA clients (e.g., user has two tabs open)
// connect to the same instance and each gets the full event stream.
//
// Frame shapes:
//   server → client: every SDK event, serialized as JSON.
//   client → server: { type: "user", message: { role: "user", content: ... } }
//                  | { type: "interrupt" }                         (cancel turn)
//                  | { type: "heartbeat", last_order_key?: string } (heartbeat/ack)
//
// Authentication is the orchestrator's job (it terminates the user's TLS
// + JWT before proxying). This server trusts whatever connects.

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

  // Broadcast a typed event by serializing once.
  broadcastEvent(message: unknown): void {
    this.broadcast(JSON.stringify(message));
  }

  close(): void {
    for (const ws of this.clients) {
      ws.close();
    }
    this.server.close();
  }
}
