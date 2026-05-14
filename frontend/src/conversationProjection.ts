import type {
  ConversationItem,
  ConversationReducerState,
  ConversationRunStatus,
} from "./conversationReducer";
import type { UserMessageDisplay } from "./tankConversation";

export type ConversationViewEntry =
  | ConversationMessageEntry
  | ConversationToolEntry
  | ConversationReasoningEntry
  | ConversationMetaEntry;

export interface ConversationMessageEntry extends ConversationEntryBase {
  kind: "message";
  role: "user" | "assistant" | "system";
  text: string;
  display?: UserMessageDisplay;
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
  sourceEventId?: string;
  orderKey?: string;
}

export interface ConversationProjection {
  entries: ConversationViewEntry[];
  runStatus: ConversationRunStatus;
  activeTurnId: string | null;
  activeItemId: string | null;
  activeToolName: string | null;
  needsInput: boolean;
  failed: boolean;
  stopped: boolean;
  lastError: string | null;
  lastUsage: unknown | null;
  lastOrderKey: string | null;
  unreadCount: number;
}

export function projectConversationState(
  state: ConversationReducerState,
): ConversationProjection {
  const entries = orderProjectedEntries([
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
            time: message.createdAt ?? "",
            sourceEventId: message.sourceEventId,
            orderKey: message.orderKey,
          },
        },
      ];
    }),
    ...state.items.flatMap((item, index) => {
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
  ]);

  const activeItem = activeToolItem(state);
  return {
    entries,
    runStatus: state.runStatus,
    activeTurnId: state.activeTurnId,
    activeItemId: activeItem?.id ?? state.activeItemId,
    activeToolName: activeItem ? toolDisplay(activeItem).toolName : null,
    needsInput: state.needsInput,
    failed: state.failed,
    stopped: state.runStatus === "stopped",
    lastError: state.lastError,
    lastUsage: state.lastUsage,
    lastOrderKey: state.lastOrderKey,
    unreadCount: state.unreadCount,
  };
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
      time: item.createdAt ?? "",
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
    time: item.createdAt ?? "",
    sourceEventId: item.sourceEventId,
    orderKey: item.orderKey,
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
  const active = state.activeItemId
    ? state.items.find((item) => item.id === state.activeItemId)
    : undefined;
  if (active && isToolLikeItem(active) && isRunningItem(active)) return active;
  for (let index = state.items.length - 1; index >= 0; index -= 1) {
    const item = state.items[index];
    if (isToolLikeItem(item) && isRunningItem(item)) return item;
  }
  return null;
}

function isRunningItem(item: ConversationItem): boolean {
  return item.status === "started" || item.status === "streaming";
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
  if (item.status === "streaming") return "running";
  return item.status;
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
