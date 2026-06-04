import type { ConversationTurnTerminalStatus } from "./conversationReducer";

export type NeedsInputAnnouncementState = "waiting" | "answered" | "settled";

export function needsInputAnnouncementState(input: {
  answered: boolean;
  turnTerminalStatus?: ConversationTurnTerminalStatus | null;
}): NeedsInputAnnouncementState {
  if (input.answered) return "answered";
  if (input.turnTerminalStatus != null) return "settled";
  return "waiting";
}

export function needsInputAnnouncementIsSettled(
  state: NeedsInputAnnouncementState,
): boolean {
  return state !== "waiting";
}
