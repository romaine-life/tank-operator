import { describe, it } from "node:test";
import assert from "node:assert";
import { GeminiTankEventAdapter } from "./gemini.js";

describe("GeminiTankEventAdapter", () => {
  const cfg = { sessionId: "session-123" } as any;
  const adapter = new GeminiTankEventAdapter(cfg);
  const turn = { turnID: "turn-456", clientNonce: "nonce-789" };

  it("should create turn.started event", () => {
    const event = adapter.turnStarted(turn);
    assert.strictEqual(event.type, "turn.started");
    assert.strictEqual(event.session_id, "session-123");
    assert.strictEqual(event.turn_id, "turn-456");
    assert.strictEqual(event.client_nonce, "nonce-789");
  });

  it("should create turn.completed event", () => {
    const event = adapter.turnCompleted(turn, { timelineIDs: ["t1"], providerItemIDs: ["p1"] });
    assert.strictEqual(event.type, "turn.completed");
    assert.deepStrictEqual(event.payload?.final_answer, { timeline_ids: ["t1"], provider_item_ids: ["p1"] });
  });

  it("should create item.completed message event", () => {
    const event = adapter.messageCompleted(turn, "p1", "hello world");
    assert.strictEqual(event.type, "item.completed");
    assert.strictEqual(event.actor, "assistant");
    assert.deepStrictEqual(event.payload, { kind: "message", text: "hello world" });
  });
});
