import type {
  ConversationBackgroundTask,
  ConversationBackgroundTaskStatus,
  ConversationItem,
  ConversationReducerState,
  ConversationRunStatus,
  ConversationTurnTerminal,
  ConversationTurnTerminalStatus,
} from "./conversationReducer";
import type { UserMessageDisplay } from "../../runner-shared/conversation.js";
import type { MessageAttachmentDisplay } from "./attachmentLabels";

export type ConversationViewEntry =
  | ConversationMessageEntry
  | ConversationToolEntry
  | ConversationBackgroundTaskEntry
  | ConversationReasoningEntry
  | ConversationMetaEntry;

export interface ConversationMessageEntry extends ConversationEntryBase {
  kind: "message";
  role: "user" | "assistant" | "system";
  text: string;
  display?: UserMessageDisplay;
  attachments?: MessageAttachmentDisplay[];
  // Set when a user-role message was posted by a sibling tank-operator
  // session via the mcp-tank-operator handoff path. The renderer uses
  // this to pick the parent session's avatar instead of the human
  // owner's Gravatar. See conversationReducer.applyUserMessage.
  originSessionId?: string;
  // Set when a user-role message was authored by a non-interactive
  // principal (an auth.romaine.life bot token). "system" tells the renderer
  // to draw the session's system identity instead of the human owner's
  // Gravatar. originSessionId takes precedence when both are present.
  authorKind?: string;
  // Severity tag for system-role messages — drives the renderer's
  // styling. Set on session.status:failed banners; absent on neutral
  // loading/ready notices. user/assistant messages ignore it.
  severity?: "info" | "error";
  // Optional actionable affordance ("Re-sign-in to Codex"). Present on
  // session.status banners that carry a payload.action; the renderer
  // surfaces it as a button on the system bubble.
  action?: { label: string; href: string };
}

export interface AskUserQuestionAnswer {
  /** The list of option labels the user selected for this question. */
  labels: string[];
  /** Optional notes the user attached to the selected option(s). */
  notes?: string;
  /** Optional preview content for the selected option (HTML fragment). */
  preview?: string;
}

export interface ConversationToolEntry extends ConversationEntryBase {
  kind: "tool";
  toolName: string;
  toolKind?: "mcp" | "shell";
  toolServer?: string;
  toolAction?: string;
  toolInput?: string;
  toolOutput?: string;
  toolStatus?: string;
  startedAt?: string;
  completedAt?: string;
  /**
   * For AskUserQuestion tools that completed via a durable input_reply
   * command, the answers the user selected — keyed by question text.
   * Sourced from the `tool.approval_resolved` event payload, not from
   * local React state, so a fresh tab opened after the answer arrived
   * still renders the user's selections.
   */
  askUserAnswers?: Record<string, AskUserQuestionAnswer>;
}

export interface ConversationBackgroundTaskEntry extends ConversationEntryBase {
  kind: "background_task";
  taskId: string;
  taskStatus: ConversationBackgroundTaskStatus;
  taskSummary?: string;
  taskDescription?: string;
  taskError?: unknown;
  taskToolUseId?: string;
  taskCommand?: string;
  taskCwd?: string;
  taskProcessId?: string;
  taskOutput?: string;
  taskExitCode?: number;
  taskDurationMs?: number;
  taskRawItem?: unknown;
  lastToolName?: string;
  startedAt?: string;
  updatedAt?: string;
  completedAt?: string;
}

export interface ConversationReasoningEntry extends ConversationEntryBase {
  kind: "reasoning";
  reasoning: { text: string };
}

export interface ConversationMetaEntry extends ConversationEntryBase {
  kind: "meta";
  meta: {
    title: string;
    detail?: string;
    severity?: "info" | "error";
  };
}

interface ConversationEntryBase {
  id: string;
  time: string;
  turnId?: string;
  clientNonce?: string;
  providerItemId?: string;
  sourceEventId?: string;
  orderKey?: string;
  turnTerminalStatus?: ConversationTurnTerminalStatus;
  turnTerminalAt?: string;
  turnTerminalEventId?: string;
}

export interface ConversationProjection {
  entries: ConversationViewEntry[];
  backgroundTasks: ConversationBackgroundTaskEntry[];
  runStatus: ConversationRunStatus;
  activeTurnId: string | null;
  activeClientNonce: string | null;
  activeItemId: string | null;
  activeToolName: string | null;
  needsInput: boolean;
  failed: boolean;
  stopping: boolean;
  stopped: boolean;
  lastError: string | null;
  lastUsage: unknown | null;
  lastOrderKey: string | null;
}

export function projectConversationState(
  state: ConversationReducerState,
): ConversationProjection {
  const backgroundProviderItemIds = backgroundTaskProviderItemIds(state);
  const entries = annotateTurnTerminals(orderProjectedEntries([
    ...state.messages.flatMap((message, index) => {
      const text = message.text.trim();
      if (!text) return [];
      return [
        {
          index,
          orderKey: message.orderKey,
          entry: {
            id: message.id,
            kind: "message" as const,
            role: message.role,
	            text,
	            display: message.display,
	            attachments: message.attachments,
	            turnId: message.turnId,
            clientNonce: message.clientNonce,
            time: message.createdAt ?? "",
            sourceEventId: message.sourceEventId,
            orderKey: message.orderKey,
            ...(message.originSessionId ? { originSessionId: message.originSessionId } : {}),
            ...(message.authorKind ? { authorKind: message.authorKind } : {}),
            ...(message.severity ? { severity: message.severity } : {}),
            ...(message.action ? { action: message.action } : {}),
          },
        },
      ];
    }),
    ...state.items.flatMap((item, index) => {
      if (item.providerItemId && backgroundProviderItemIds.has(item.providerItemId)) {
        return [];
      }
      const entry = projectItem(item);
      return entry
        ? [
            {
              index: state.messages.length + index,
              orderKey: item.orderKey,
              entry,
            },
          ]
        : [];
    }),
    ...state.backgroundTasks.map((task, index) => {
      const entry = projectBackgroundTask(task);
      return {
        index: state.messages.length + state.items.length + index,
        orderKey: task.orderKey,
        entry,
      };
    }),
    ...state.interruptRequests.map((request, index) => ({
      index: state.messages.length + state.items.length + state.backgroundTasks.length + index,
      orderKey: request.orderKey,
      entry: {
        id: request.id,
        kind: "meta" as const,
        meta: {
          title: "Stop requested",
          detail: "Terminating the active turn.",
          severity: "info" as const,
        },
        turnId: request.turnId,
        clientNonce: request.clientNonce,
        time: request.time,
        sourceEventId: request.id,
        orderKey: request.orderKey,
      },
    })),
    // Per-turn terminal failures and interruptions become durable transcript
    // meta lines, replacing the floating run-status pill. turn.completed
    // emits no meta entry — successful completion speaks through the
    // assistant bubble itself.
    ...turnTerminalMetaEntries(state),
  ]), state.turnTerminals);

  const activeItem = activeToolItem(state);
  const backgroundTasks = annotateTurnTerminals(orderProjectedEntries(
    state.backgroundTasks.map((task, index) => ({
      index,
      orderKey: task.orderKey,
      entry: projectBackgroundTask(task),
    })),
  ), state.turnTerminals).filter((entry): entry is ConversationBackgroundTaskEntry => entry.kind === "background_task");
  return {
    entries,
    backgroundTasks,
    runStatus: state.runStatus,
    activeTurnId: state.activeTurnId,
    activeClientNonce: activeClientNonceForTurn(state, state.activeTurnId),
    activeItemId: activeItem?.id ?? state.activeItemId,
    activeToolName: activeItem ? toolDisplay(activeItem).toolName : null,
    needsInput: state.needsInput,
    failed: state.failed,
    stopping: state.runStatus === "stopping",
    stopped: state.runStatus === "stopped",
    lastError: state.lastError,
    lastUsage: state.lastUsage,
    lastOrderKey: state.lastOrderKey,
  };
}

function activeClientNonceForTurn(
  state: ConversationReducerState,
  turnId: string | null,
): string | null {
  if (!turnId) return null;
  for (let index = state.messages.length - 1; index >= 0; index -= 1) {
    const message = state.messages[index];
    if (message.turnId === turnId && message.clientNonce) {
      return message.clientNonce;
    }
  }
  return null;
}

function projectItem(item: ConversationItem): ConversationViewEntry | null {
  if (isAssistantMessageItem(item)) {
    const text = item.text?.trim() ?? "";
    if (!text) return null;
    return {
      id: item.id,
      kind: "message",
      role: "assistant",
      text,
      turnId: item.turnId,
      providerItemId: item.providerItemId,
      time: item.createdAt ?? "",
      sourceEventId: item.sourceEventId,
      orderKey: item.orderKey,
    };
  }

  if (item.kind === "reasoning") {
    const text = item.text?.trim() || stringPayload(item, "text") || "";
    if (!text) return null;
    return {
      id: item.id,
      kind: "reasoning",
      reasoning: { text },
      turnId: item.turnId,
      providerItemId: item.providerItemId,
      time: item.createdAt ?? "",
      sourceEventId: item.sourceEventId,
      orderKey: item.orderKey,
    };
  }

  if (isToolLikeItem(item)) {
    const display = toolDisplay(item);
    return {
      id: item.id,
      kind: "tool",
      ...display,
      toolInput: toolInput(item),
      toolOutput: toolOutput(item),
      toolStatus: toolStatus(item),
      askUserAnswers: askUserAnswers(item),
      turnId: item.turnId,
      providerItemId: item.providerItemId,
      time: item.startedAt ?? item.createdAt ?? "",
      startedAt: item.startedAt ?? item.createdAt,
      completedAt: item.completedAt,
      sourceEventId: item.sourceEventId,
      orderKey: item.orderKey,
    };
  }

  const text = item.text?.trim() ?? "";
  if (!text) return null;
  return {
    id: item.id,
    kind: "meta",
    meta: {
      title: item.title ?? item.kind,
      detail: text,
      severity: item.status === "failed" ? "error" : "info",
    },
    turnId: item.turnId,
    providerItemId: item.providerItemId,
    time: item.createdAt ?? "",
    sourceEventId: item.sourceEventId,
    orderKey: item.orderKey,
  };
}

function projectBackgroundTask(task: ConversationBackgroundTask): ConversationBackgroundTaskEntry {
  return {
    id: task.id,
    kind: "background_task",
    taskId: task.taskId,
    taskStatus: task.status,
    taskSummary: task.summary,
    taskDescription: task.description,
    taskError: task.error,
    taskToolUseId: task.toolUseId,
    taskCommand: task.command,
    taskCwd: task.cwd,
    taskProcessId: task.processId,
    taskOutput: task.output,
    taskExitCode: task.exitCode,
    taskDurationMs: task.durationMs,
    taskRawItem: task.rawItem,
    lastToolName: task.lastToolName,
    turnId: task.turnId,
    providerItemId: task.providerItemId,
    time: task.startedAt ?? task.createdAt ?? task.updatedAt ?? "",
    startedAt: task.startedAt ?? task.createdAt,
    updatedAt: task.updatedAt,
    completedAt: task.completedAt,
    sourceEventId: task.sourceEventId,
    orderKey: task.orderKey,
  };
}

function orderProjectedEntries(
  items: Array<{ entry: ConversationViewEntry; orderKey?: string; index: number }>,
): ConversationViewEntry[] {
  return [...items]
    .sort((a, b) => {
      const order = compareNullableString(a.orderKey, b.orderKey);
      return order !== 0 ? order : a.index - b.index;
    })
    .map((item) => item.entry);
}

function annotateTurnTerminals(
  entries: ConversationViewEntry[],
  terminals: Record<string, ConversationTurnTerminal>,
): ConversationViewEntry[] {
  if (Object.keys(terminals).length === 0) return entries;
  return entries.map((entry) => {
    if (!entry.turnId) return entry;
    const terminal = terminals[entry.turnId];
    if (!terminal) return entry;
    return {
      ...entry,
      turnTerminalStatus: terminal.status,
      turnTerminalAt: terminal.time,
      turnTerminalEventId: terminal.sourceEventId,
    };
  });
}

function turnTerminalMetaEntries(
  state: ConversationReducerState,
): Array<{ index: number; orderKey: string | undefined; entry: ConversationViewEntry }> {
  const baseIndex =
    state.messages.length +
    state.items.length +
    state.backgroundTasks.length +
    state.interruptRequests.length;
  const out: Array<{ index: number; orderKey: string | undefined; entry: ConversationViewEntry }> = [];
  let offset = 0;
  for (const terminal of Object.values(state.turnTerminals)) {
    if (terminal.status === "completed") continue;
    const isFailed = terminal.status === "failed";
    const title = isFailed ? "Turn failed" : "Stopped";
    const detail = isFailed
      ? terminal.detail ?? "The provider returned an error."
      : "Turn stopped by user.";
    out.push({
      index: baseIndex + offset,
      orderKey: terminal.orderKey,
      entry: {
        id: `turn-terminal:${terminal.sourceEventId}`,
        kind: "meta",
        meta: {
          title,
          detail,
          severity: isFailed ? "error" : "info",
        },
        turnId: terminal.turnId,
        clientNonce: terminal.clientNonce,
        time: terminal.time,
        sourceEventId: terminal.sourceEventId,
        orderKey: terminal.orderKey,
      },
    });
    offset += 1;
  }
  return out;
}

function compareNullableString(a: string | undefined, b: string | undefined): number {
  if (a && b) return a.localeCompare(b);
  if (a) return -1;
  if (b) return 1;
  return 0;
}

function isAssistantMessageItem(item: ConversationItem): boolean {
  return (
    item.actor === "assistant" &&
    (item.kind === "message" || item.kind === "agent_message")
  );
}

function isToolLikeItem(item: ConversationItem): boolean {
  return (
    item.actor === "tool" ||
    item.kind === "tool" ||
    item.kind === "tool_result" ||
    item.kind === "approval" ||
    item.kind === "needs_input" ||
    item.kind === "command_execution" ||
    item.kind === "file_change" ||
    item.kind === "mcp_tool_call" ||
    item.kind === "web_search"
  );
}

function activeToolItem(state: ConversationReducerState): ConversationItem | null {
  const backgroundProviderItemIds = backgroundTaskProviderItemIds(state);
  const active = state.activeItemId
    ? state.items.find((item) => item.id === state.activeItemId)
    : undefined;
  if (
    active &&
    isToolLikeItem(active) &&
    isRunningItem(active) &&
    !isBackgroundProviderItem(active, backgroundProviderItemIds)
  ) {
    return active;
  }
  for (let index = state.items.length - 1; index >= 0; index -= 1) {
    const item = state.items[index];
    if (
      isToolLikeItem(item) &&
      isRunningItem(item) &&
      !isBackgroundProviderItem(item, backgroundProviderItemIds)
    ) {
      return item;
    }
  }
  return null;
}

function backgroundTaskProviderItemIds(state: ConversationReducerState): Set<string> {
  const ids = new Set<string>();
  for (const task of state.backgroundTasks) {
    if (task.providerItemId) ids.add(task.providerItemId);
    if (task.toolUseId) ids.add(task.toolUseId);
  }
  return ids;
}

function isBackgroundProviderItem(
  item: ConversationItem,
  backgroundProviderItemIds: Set<string>,
): boolean {
  return Boolean(item.providerItemId && backgroundProviderItemIds.has(item.providerItemId));
}

function isRunningItem(item: ConversationItem): boolean {
  return item.status === "started";
}

function toolDisplay(item: ConversationItem): Pick<
  ConversationToolEntry,
  "toolName" | "toolKind" | "toolServer" | "toolAction"
> {
  const raw = recordPayload(item, "raw_item");
  const rawServer = stringRecordValue(raw, "server");
  const rawTool = stringRecordValue(raw, "tool");
  const payloadServer = stringPayload(item, "server");
  const payloadTool = stringPayload(item, "tool");
  const server = payloadServer ?? rawServer;
  const action = payloadTool ?? rawTool;

  if (item.kind === "mcp_tool_call" || server || action) {
    const toolAction = action ?? item.title ?? "tool";
    const toolServer = server ?? "mcp";
    return {
      toolName: `${toolServer}.${toolAction}`,
      toolKind: "mcp",
      toolServer,
      toolAction,
    };
  }

  const name =
    stringPayload(item, "name") ??
    item.title ??
    stringPayload(item, "title") ??
    stringPayload(item, "command") ??
    item.kind;
  const mcpMatch = /^mcp__([^_]+)__(.+)$/.exec(name);
  if (mcpMatch) {
    return {
      toolName: name,
      toolKind: "mcp",
      toolServer: mcpMatch[1],
      toolAction: mcpMatch[2],
    };
  }
  return {
    toolName: name,
    ...(item.kind === "command_execution" || name === "Bash" ? { toolKind: "shell" as const } : {}),
  };
}

function toolInput(item: ConversationItem): string | undefined {
  const raw = recordPayload(item, "raw_item");
  return formatPayloadValue(
    item.payload?.input ??
      item.payload?.arguments ??
      item.payload?.command ??
      raw?.arguments ??
      raw?.changes ??
      raw?.command,
  );
}

function toolOutput(item: ConversationItem): string | undefined {
  const raw = recordPayload(item, "raw_item");
  return formatPayloadValue(
    item.payload?.output ??
      item.payload?.result ??
      item.payload?.error ??
      raw?.aggregated_output ??
      raw?.result ??
      raw?.error,
  );
}

function toolStatus(item: ConversationItem): string {
  return item.status;
}

// askUserAnswers reads the durable answers/annotations off a
// merged AskUserQuestion item payload. The reducer merges
// `tool.approval_requested` (carrying `input.questions[]`) and
// `tool.approval_resolved` (carrying `answers` / `annotations`) into the
// same `ConversationItem` payload, so by the time the item reaches
// projection both halves are present.
function askUserAnswers(item: ConversationItem): Record<string, AskUserQuestionAnswer> | undefined {
  const rawAnswers = item.payload?.answers;
  if (!rawAnswers || typeof rawAnswers !== "object" || Array.isArray(rawAnswers)) {
    return undefined;
  }
  const rawAnnotations = item.payload?.annotations;
  const annotations =
    rawAnnotations && typeof rawAnnotations === "object" && !Array.isArray(rawAnnotations)
      ? (rawAnnotations as Record<string, { preview?: unknown; notes?: unknown }>)
      : undefined;
  const out: Record<string, AskUserQuestionAnswer> = {};
  for (const [question, value] of Object.entries(rawAnswers as Record<string, unknown>)) {
    if (!Array.isArray(value)) continue;
    const labels = value
      .map((entry) => (typeof entry === "string" ? entry : ""))
      .filter((entry) => entry.length > 0);
    if (labels.length === 0) continue;
    const ann = annotations?.[question];
    const entry: AskUserQuestionAnswer = { labels };
    if (typeof ann?.preview === "string" && ann.preview) entry.preview = ann.preview;
    if (typeof ann?.notes === "string" && ann.notes) entry.notes = ann.notes;
    out[question] = entry;
  }
  return Object.keys(out).length > 0 ? out : undefined;
}

function stringPayload(item: ConversationItem, key: string): string | undefined {
  const value = item.payload?.[key];
  return typeof value === "string" ? value : undefined;
}

function recordPayload(item: ConversationItem, key: string): Record<string, unknown> | undefined {
  const value = item.payload?.[key];
  return value && typeof value === "object" && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : undefined;
}

function stringRecordValue(
  record: Record<string, unknown> | undefined,
  key: string,
): string | undefined {
  const value = record?.[key];
  return typeof value === "string" && value ? value : undefined;
}

function formatPayloadValue(value: unknown): string | undefined {
  if (value === undefined || value === null) return undefined;
  if (typeof value === "string") return value;
  try {
    return JSON.stringify(value, null, 2);
  } catch {
    return String(value);
  }
}
