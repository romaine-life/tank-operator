import type { Config } from "../config.js";
import type { TankConversationEvent } from "../../../runner-shared/conversation.js";
import { itemEvent, turnEvent } from "../../../runner-shared/conversation-builders.js";

export interface GeminiAdapterTurn {
  turnID: string;
  clientNonce: string;
}

export class GeminiTankEventAdapter {
  constructor(private readonly cfg: Config) {}

  turnStarted(turn: GeminiAdapterTurn): TankConversationEvent {
    return turnEvent({
      sessionID: this.cfg.sessionId,
      turnID: turn.turnID,
      clientNonce: turn.clientNonce,
      source: "gemini",
      type: "turn.started",
    });
  }

  turnCompleted(
    turn: GeminiAdapterTurn,
    finalAnswer?: { timelineIDs: string[]; providerItemIDs: string[] }
  ): TankConversationEvent {
    return turnEvent({
      sessionID: this.cfg.sessionId,
      turnID: turn.turnID,
      clientNonce: turn.clientNonce,
      source: "gemini",
      type: "turn.completed",
      finalAnswer,
    });
  }

  turnFailed(turn: GeminiAdapterTurn, error: string): TankConversationEvent {
    return turnEvent({
      sessionID: this.cfg.sessionId,
      turnID: turn.turnID,
      clientNonce: turn.clientNonce,
      source: "gemini",
      type: "turn.failed",
      reason: "provider_failure",
      error,
    });
  }

  turnInterrupted(turn: GeminiAdapterTurn): TankConversationEvent {
    return turnEvent({
      sessionID: this.cfg.sessionId,
      turnID: turn.turnID,
      clientNonce: turn.clientNonce,
      source: "gemini",
      type: "turn.interrupted",
      reason: "client_interrupt",
    });
  }

  messageCompleted(
    turn: GeminiAdapterTurn,
    providerItemID: string,
    text: string
  ): TankConversationEvent {
    return itemEvent({
      sessionID: this.cfg.sessionId,
      turnID: turn.turnID,
      source: "gemini",
      type: "item.completed",
      providerItemID,
      actor: "assistant",
      payload: { kind: "message", text },
    });
  }

  toolStarted(
    turn: GeminiAdapterTurn,
    providerItemID: string,
    name: string,
    input: any
  ): TankConversationEvent {
    return itemEvent({
      sessionID: this.cfg.sessionId,
      turnID: turn.turnID,
      source: "gemini",
      type: "item.started",
      providerItemID,
      actor: "tool",
      payload: {
        kind: "tool",
        title: name,
        name,
        input,
      },
    });
  }

  toolCompleted(
    turn: GeminiAdapterTurn,
    providerItemID: string,
    name: string,
    input: any,
    result: any
  ): TankConversationEvent {
    return itemEvent({
      sessionID: this.cfg.sessionId,
      turnID: turn.turnID,
      source: "gemini",
      type: "item.completed",
      providerItemID,
      actor: "tool",
      payload: {
        kind: "tool",
        title: name,
        name,
        input,
        result,
      },
    });
  }

  toolFailed(
    turn: GeminiAdapterTurn,
    providerItemID: string,
    name: string,
    input: any,
    error: string
  ): TankConversationEvent {
    return itemEvent({
      sessionID: this.cfg.sessionId,
      turnID: turn.turnID,
      source: "gemini",
      type: "item.failed",
      providerItemID,
      actor: "tool",
      payload: {
        kind: "tool",
        title: name,
        name,
        input,
        error,
      },
    });
  }
}
