import type { TranscriptEntry as SandboxTranscriptEntry } from "@sandbox-agent/react";
import { isTankConversationEvent, type TankConversationEvent } from "./tankConversation";

export type ToolKind = "mcp" | "shell";

export type ProviderTranscriptEntry = SandboxTranscriptEntry & {
  toolKind?: ToolKind;
  toolServer?: string;
  toolAction?: string;
  transcriptSource?: "server" | "realtime";
  sourceEventId?: string;
  orderKey?: string;
  localOnly?: boolean;
  clientNonce?: string;
};

export type JsonObject = Record<string, unknown>;

export interface ProviderFrameEffects {
  usage?: unknown;
  activeTool?: {
    name: string | null;
    id?: string | null;
  };
  completedToolUseId?: string | null;
}

export function isJsonObject(value: unknown): value is JsonObject {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

export function applyProviderEvent(
  entries: ProviderTranscriptEntry[],
  mode: string,
  event: JsonObject,
): ProviderTranscriptEntry[] {
  if (isTankConversationEvent(event)) {
    return applyCanonicalConversationEvent(entries, event);
  }
  if (event.type === "tank.user_message") {
    return applyTankUserMessageEvent(entries, event);
  }
  if (event.type === "tank.skill_invocation") {
    const name = typeof event.name === "string" ? event.name.trim() : "";
    const trigger = typeof event.trigger === "string" ? event.trigger : "";
    return name
      ? appendSkillInvocation(entries, name, skillSupplementalText(name, trigger), eventTime(event))
      : entries;
  }
  if (mode === "codex_gui") return applyCodexEvent(entries, event);
  return applyClaudeEvent(entries, event);
}

export function parseProviderRunHistory(
  text: string,
  mode: string,
): ProviderTranscriptEntry[] {
  let acc: ProviderTranscriptEntry[] = [];
  for (const line of text.split("\n")) {
    if (!line.trim()) continue;
    try {
      const event = JSON.parse(line);
      if (isJsonObject(event)) {
        acc = applyProviderEvent(acc, mode, event);
      }
    } catch {
      /* skip unparseable history lines */
    }
  }
  return acc;
}

export function providerFrameEffects(event: JsonObject): ProviderFrameEffects {
  const type = event.type;
  if (type === "assistant") {
    const message = event.message;
    if (!isJsonObject(message)) return {};
    const effects: ProviderFrameEffects = { usage: message.usage };
    if (Array.isArray(message.content)) {
      const toolBlock = message.content.find(
        (block): block is JsonObject => isJsonObject(block) && block.type === "tool_use",
      );
      effects.activeTool = {
        name: toolBlock && typeof toolBlock.name === "string" ? toolBlock.name : null,
        id: toolBlock && typeof toolBlock.id === "string" ? toolBlock.id : null,
      };
    }
    return effects;
  }
  if (type === "user") {
    return { completedToolUseId: null };
  }
  if (type === "result") {
    return { usage: event.usage, activeTool: { name: null } };
  }
  if (type === "item.started" || type === "item.updated") {
    const item = event.item;
    if (!isJsonObject(item)) return {};
    const itemType = typeof item.type === "string" ? item.type : "";
    if (!isCodexToolishItemType(itemType)) return {};
    return {
      activeTool: {
        name: itemType,
        id: typeof item.id === "string" ? item.id : null,
      },
    };
  }
  if (type === "item.completed") {
    const item = event.item;
    if (!isJsonObject(item)) return {};
    const itemType = typeof item.type === "string" ? item.type : "";
    if (!isCodexToolishItemType(itemType)) return {};
    return {
      completedToolUseId: typeof item.id === "string" ? item.id : null,
    };
  }
  if (type === "turn.completed") {
    return { usage: event.usage, activeTool: { name: null } };
  }
  return {};
}

export function isProviderAbortMessage(message: unknown): boolean {
  return typeof message === "string" && /operation was aborted/i.test(message);
}

function applyCanonicalConversationEvent(
  entries: ProviderTranscriptEntry[],
  event: TankConversationEvent,
): ProviderTranscriptEntry[] {
  if (event.type === "user_message.created") {
    return applyTankUserMessageEvent(entries, event);
  }
  return entries;
}

function applyTankUserMessageEvent(
  entries: ProviderTranscriptEntry[],
  event: TankConversationEvent | JsonObject,
): ProviderTranscriptEntry[] {
  const text = tankUserMessageText(event);
  if (!text || hasUserMessageText(entries, text)) return entries;
  const skillName = skillNameFromTrigger(text);
  if (skillName && hasSkillInvocation(entries, skillName)) return entries;
  return upsertEntry(entries, {
    id: `tank-user-message-${eventID(event as JsonObject, "tank-user-message")}`,
    kind: "message",
    role: "user",
    text,
    time: eventTime(event as JsonObject),
  });
}

function applyCodexEvent(
  entries: ProviderTranscriptEntry[],
  event: JsonObject,
): ProviderTranscriptEntry[] {
  const type = event.type;
  const time = eventTime(event);
  if (type === "thread.started") {
    const threadId = typeof event.thread_id === "string" ? event.thread_id : "";
    return appendMeta(entries, `codex-thread-${eventID(event, threadId || "codex-thread")}`, "Codex thread started", threadId, "info", time);
  }
  if (type === "turn.started") {
    return appendMeta(entries, `codex-turn-started-${eventID(event, "codex-turn-started")}`, "Turn started", undefined, "info", time);
  }
  if (type === "turn.completed") {
    return appendMeta(entries, `codex-turn-completed-${eventID(event, "codex-turn-completed")}`, "Turn completed", describeUsage(event.usage), "info", time);
  }
  if (type === "turn.interrupted") {
    return appendMeta(
      entries,
      `codex-turn-interrupted-${eventID(event, "codex-turn-interrupted")}`,
      "Turn stopped",
      typeof event.reason === "string" ? event.reason : undefined,
      "info",
      time,
    );
  }
  if (type === "turn.failed" || type === "error") {
    const error = isJsonObject(event.error) ? event.error.message : event.message;
    if (isProviderAbortMessage(error)) {
      return appendMeta(
        entries,
        `codex-turn-stopped-${eventID(event, "codex-turn-stopped")}`,
        "Turn stopped",
        "The turn was interrupted before completion.",
        "info",
        time,
      );
    }
    return appendMeta(
      entries,
      `codex-error-${eventID(event, "codex-error")}`,
      type === "turn.failed" ? "Turn failed" : "Codex error",
      typeof error === "string" ? error : shortJson(event),
      "error",
      time,
    );
  }
  if (type === "item.started" || type === "item.updated" || type === "item.completed") {
    const item = event.item;
    if (!isJsonObject(item)) return entries;
    if (item.type === "agent_message") {
      return appendAssistantMessage(
        entries,
        codexItemEntryId(event, item, "codex-message"),
        typeof item.text === "string" ? item.text : "",
        time,
      );
    }
    if (item.type === "reasoning") {
      return upsertEntry(entries, {
        id: codexItemEntryId(event, item, "codex-reasoning"),
        kind: "reasoning",
        time,
        reasoning: { text: typeof item.text === "string" ? item.text : shortJson(item) },
      });
    }
    const toolEntry = codexToolEntry(event);
    return toolEntry ? upsertEntry(entries, toolEntry) : entries;
  }
  return appendMeta(entries, `codex-event-${eventID(event, "codex-event")}`, String(type || "Codex event"), shortJson(event), "info", time);
}

function applyClaudeEvent(
  entries: ProviderTranscriptEntry[],
  event: JsonObject,
): ProviderTranscriptEntry[] {
  const type = event.type;
  const time = eventTime(event);
  if (
    type === "system" ||
    type === "stream_event" ||
    type === "rate_limit_event" ||
    type === "ai-title" ||
    type === "last-prompt" ||
    type === "attachment" ||
    type === "queue-operation"
  ) {
    return entries;
  }
  if (type === "assistant") {
    let nextEntries = entries;
    for (const toolEntry of claudeToolEntries(event)) {
      nextEntries = upsertEntry(nextEntries, toolEntry);
    }
    const message = event.message;
    const text = isJsonObject(message) ? claudeTextFromContent(message.content, false) : "";
    if (text) {
      nextEntries = appendAssistantMessage(
        nextEntries,
        typeof event.uuid === "string" ? event.uuid : `claude-message-${Date.now()}`,
        text,
        time,
      );
    }
    return nextEntries;
  }
  if (type === "user") {
    let nextEntries = applyClaudeToolResults(entries, event);
    const message = event.message;
    if (isJsonObject(message)) {
      const texts: string[] = [];
      if (typeof message.content === "string") {
        const text = message.content.trim();
        if (text) texts.push(text);
      } else if (Array.isArray(message.content)) {
        const hasToolResults = message.content.some(
          (block) => isJsonObject(block) && block.type === "tool_result",
        );
        if (!hasToolResults) {
          for (const block of message.content) {
            if (!isJsonObject(block) || block.type !== "text") continue;
            const text = typeof block.text === "string" ? block.text.trim() : "";
            if (text) texts.push(text);
          }
        }
      }
      const baseId = eventID(event, "claude-user-message");
      for (const [index, text] of texts.entries()) {
        const skillName = skillNameFromTrigger(text);
        if (skillName && hasSkillInvocation(nextEntries, skillName)) continue;
        if (hasUserMessageText(nextEntries, text)) continue;
        nextEntries = upsertEntry(nextEntries, {
          id: `${baseId}-${index}`,
          kind: "message",
          role: "user",
          text,
          time,
        });
      }
    }
    return nextEntries;
  }
  if (type === "result") {
    const isError = event.is_error === true || event.subtype === "error";
    const result = typeof event.result === "string" ? event.result : "";
    const id = eventID(event, "claude-result");
    if (!isError) {
      if (result && !entries.some((entry) => entry.kind === "message" && entry.text === result)) {
        return appendAssistantMessage(entries, `claude-result-message-${id}`, result, time);
      }
      return entries;
    }
    let nextEntries = appendMeta(
      entries,
      `claude-result-${id}`,
      "Claude run failed",
      result,
      "error",
      time,
    );
    if (result && !entries.some((entry) => entry.kind === "message" && entry.text === result)) {
      nextEntries = appendAssistantMessage(nextEntries, `claude-result-message-${id}`, result, time);
    }
    return nextEntries;
  }
  return appendMeta(entries, `claude-event-${eventID(event, "claude-event")}`, String(type || "Claude event"), shortJson(event), "info", time);
}

function upsertEntry(
  entries: ProviderTranscriptEntry[],
  entry: ProviderTranscriptEntry,
): ProviderTranscriptEntry[] {
  const index = entries.findIndex((candidate) => candidate.id === entry.id);
  if (index === -1) return [...entries, entry];
  const next = [...entries];
  next[index] = { ...next[index], ...entry };
  return next;
}

function appendMeta(
  entries: ProviderTranscriptEntry[],
  id: string,
  title: string,
  detail?: string,
  severity: "info" | "error" = "info",
  time: string = nowIso(),
): ProviderTranscriptEntry[] {
  return upsertEntry(entries, {
    id,
    kind: "meta",
    meta: { title, detail, severity },
    time,
  });
}

function appendAssistantMessage(
  entries: ProviderTranscriptEntry[],
  id: string,
  text: string,
  time: string = nowIso(),
): ProviderTranscriptEntry[] {
  if (!text.trim()) return entries;
  return upsertEntry(entries, {
    id,
    kind: "message",
    role: "assistant",
    text,
    time,
  });
}

function appendSkillInvocation(
  entries: ProviderTranscriptEntry[],
  name: string,
  supplementalText = "",
  time: string = nowIso(),
): ProviderTranscriptEntry[] {
  if (!name) return entries;
  const suffix = Date.parse(time) || Date.now();
  const userAction = {
    id: `skill-action-${name}-${suffix}`,
    kind: "message" as const,
    role: "user" as const,
    text: skillActionText(name),
    time,
    messageKind: "skill-action",
    skillName: name,
    skillSupplementalText: supplementalText.trim(),
  } as ProviderTranscriptEntry;
  return appendMeta(
    [...entries, userAction],
    `skill-invocation-${name}-${suffix}`,
    skillInvocationTitle(name),
    undefined,
    "info",
    time,
  );
}

function codexToolEntry(event: JsonObject): ProviderTranscriptEntry | null {
  const item = event.item;
  if (!isJsonObject(item)) return null;
  const id = codexItemEntryId(event, item, "codex-tool");
  const status = codexItemStatus(event, item);
  const itemType = item.type;
  const time = eventTime(event);

  if (itemType === "command_execution") {
    const command = typeof item.command === "string" ? item.command : "command";
    return {
      id,
      kind: "tool",
      toolKind: "shell",
      toolName: command,
      toolInput: command,
      toolOutput: shortJson(item.aggregated_output),
      toolStatus: status,
      time,
    };
  }

  if (itemType === "file_change") {
    return {
      id,
      kind: "tool",
      toolName: "file change",
      toolInput: shortJson(item.changes),
      toolStatus: status,
      time,
    };
  }

  if (itemType === "mcp_tool_call") {
    const server = typeof item.server === "string" ? item.server : "mcp";
    const tool = typeof item.tool === "string" ? item.tool : "tool";
    return {
      id,
      kind: "tool",
      toolKind: "mcp",
      toolServer: server,
      toolAction: tool,
      toolName: `${server}.${tool}`,
      toolInput: shortJson(item.arguments),
      toolOutput: shortJson(item.result ?? item.error),
      toolStatus: status,
      time,
    };
  }

  if (itemType === "web_search") {
    return {
      id,
      kind: "tool",
      toolName: "web search",
      toolInput: typeof item.query === "string" ? item.query : shortJson(item),
      toolStatus: status,
      time,
    };
  }

  return null;
}

function claudeToolEntries(event: JsonObject): ProviderTranscriptEntry[] {
  const message = event.message;
  if (!isJsonObject(message) || !Array.isArray(message.content)) return [];
  const time = eventTime(event);
  return message.content.flatMap((block): ProviderTranscriptEntry[] => {
    if (!isJsonObject(block) || block.type !== "tool_use") return [];
    const id = typeof block.id === "string" ? block.id : `claude-tool-${Date.now()}`;
    const toolName = typeof block.name === "string" ? block.name : "tool";
    const mcpMatch = /^mcp__([^_]+)__(.+)$/.exec(toolName);
    const toolKind = mcpMatch ? "mcp" : toolName === "Bash" ? "shell" : undefined;
    return [
      {
        id,
        kind: "tool",
        toolName,
        ...(toolKind
          ? {
              toolKind,
              ...(mcpMatch
                ? {
                    toolServer: mcpMatch[1],
                    toolAction: mcpMatch[2],
                  }
                : {}),
            }
          : {}),
        toolInput: shortJson(block.input),
        toolStatus: "started",
        time,
      },
    ];
  });
}

function applyClaudeToolResults(
  entries: ProviderTranscriptEntry[],
  event: JsonObject,
): ProviderTranscriptEntry[] {
  const message = event.message;
  if (!isJsonObject(message) || !Array.isArray(message.content)) return entries;
  const time = eventTime(event);
  return message.content.reduce<ProviderTranscriptEntry[]>((nextEntries, block) => {
    if (!isJsonObject(block) || block.type !== "tool_result") return nextEntries;
    const toolUseId = typeof block.tool_use_id === "string" ? block.tool_use_id : "";
    if (!toolUseId) return nextEntries;
    const existing = nextEntries.find((entry) => entry.id === toolUseId);
    return upsertEntry(nextEntries, {
      id: toolUseId,
      kind: "tool",
      toolName: existing?.toolName ?? "tool result",
      toolInput: existing?.toolInput,
      toolOutput: toolResultText(block.content),
      toolStatus: block.is_error === true ? "failed" : "completed",
      time: existing?.time ?? time,
    });
  }, entries);
}

function tankUserMessageText(event: TankConversationEvent | JsonObject): string {
  const payload = (event as { payload?: unknown }).payload;
  if (isJsonObject(payload)) {
    if (typeof payload.text === "string") return payload.text.trim();
    if (typeof payload.message === "string") return payload.message.trim();
    if (isJsonObject(payload.message)) {
      const content = payload.message.content;
      if (typeof content === "string") return content.trim();
    }
  }
  const message = (event as { message?: unknown }).message;
  if (typeof message === "string") return message.trim();
  if (isJsonObject(message)) {
    const content = message.content;
    if (typeof content === "string") return content.trim();
    if (Array.isArray(content)) {
      return content
        .map((block) =>
          isJsonObject(block) && block.type === "text" && typeof block.text === "string"
            ? block.text
            : "",
        )
        .filter(Boolean)
        .join("\n")
        .trim();
    }
  }
  return "";
}

function claudeTextFromContent(content: unknown, includeToolResults: boolean): string {
  if (!Array.isArray(content)) return "";
  return content
    .map((block) => {
      if (!isJsonObject(block)) return "";
      if (block.type === "text" && typeof block.text === "string") return block.text;
      if (includeToolResults && block.type === "tool_result") return shortJson(block.content);
      return "";
    })
    .filter(Boolean)
    .join("\n\n");
}

function toolResultText(content: unknown): string {
  if (typeof content === "string") return content;
  if (Array.isArray(content)) {
    const text = content
      .map((block) =>
        isJsonObject(block) && block.type === "text" && typeof block.text === "string"
          ? block.text
          : "",
      )
      .filter(Boolean)
      .join("\n");
    return text || shortJson(content);
  }
  return shortJson(content);
}

function codexItemEntryId(event: JsonObject, item: JsonObject, fallbackPrefix: string): string {
  const itemId = typeof item.id === "string" && item.id ? item.id : "";
  if (itemId) return `${fallbackPrefix}-${codexTurnScope(event)}-${itemId}`;
  return `${fallbackPrefix}-${eventID(event, fallbackPrefix)}`;
}

function codexTurnScope(event: JsonObject): string {
  const turnSeq = event.tank_turn_seq;
  if (typeof turnSeq === "number" && Number.isFinite(turnSeq)) return String(turnSeq);
  if (typeof turnSeq === "string" && turnSeq) return turnSeq;
  return eventID(event, "codex-event");
}

function codexItemStatus(event: JsonObject, item: JsonObject): string {
  if (typeof item.status === "string" && item.status) return item.status;
  if (event.type === "item.started") return "started";
  if (event.type === "item.updated") return "running";
  if (event.type === "item.completed") {
    return item.error ? "failed" : "completed";
  }
  return String(event.type ?? "");
}

function isCodexToolishItemType(itemType: string): boolean {
  return (
    itemType === "command_execution" ||
    itemType === "file_change" ||
    itemType === "mcp_tool_call" ||
    itemType === "web_search"
  );
}

function describeUsage(usage: unknown): string {
  if (!isJsonObject(usage)) return "";
  const input = usage.input_tokens;
  const cached = usage.cached_input_tokens;
  const output = usage.output_tokens;
  const reasoning = usage.reasoning_output_tokens;
  return [
    typeof input === "number" ? `input ${input}` : null,
    typeof cached === "number" ? `cached ${cached}` : null,
    typeof output === "number" ? `output ${output}` : null,
    typeof reasoning === "number" ? `reasoning ${reasoning}` : null,
  ]
    .filter(Boolean)
    .join(" · ");
}

function skillInvocationTitle(name: string): string {
  return `You started ${name} skill`;
}

function skillActionText(name: string): string {
  return `${name.charAt(0).toUpperCase()}${name.slice(1)} skill`;
}

function skillSupplementalText(name: string, text: string): string {
  const trimmed = text.trim();
  const triggerPattern = new RegExp(`^[$/]${name}(?:\\s+|\\n+)?`, "i");
  return trimmed.replace(triggerPattern, "").trim();
}

function skillNameFromTrigger(text: string): string | null {
  const trimmed = text.trim();
  const match = trimmed.match(/^[$/]([A-Za-z0-9_-]{1,64})$/);
  return match ? match[1] : null;
}

function hasSkillInvocation(entries: ProviderTranscriptEntry[], name: string): boolean {
  return entries.some(
    (entry) =>
      entry.kind === "meta" &&
      entry.meta?.title === skillInvocationTitle(name),
  );
}

function hasUserMessageText(entries: ProviderTranscriptEntry[], text: string): boolean {
  const normalized = text.trim();
  return Boolean(
    normalized &&
      entries.some(
        (entry) =>
          entry.kind === "message" &&
          entry.role === "user" &&
          entry.text?.trim() === normalized,
      ),
  );
}

function eventID(event: JsonObject, fallbackPrefix: string): string {
  if (typeof event.uuid === "string" && event.uuid) return event.uuid;
  if (typeof event.id === "string" && event.id) return event.id;
  if (typeof event.event_id === "string" && event.event_id) return event.event_id;
  if (typeof event.order_key === "string" && event.order_key) {
    return event.order_key;
  }
  if (typeof event.tank_order_key === "string" && event.tank_order_key) {
    return event.tank_order_key;
  }
  return `${fallbackPrefix}-${eventTime(event)}-${stableStringHash(shortJson(event))}`;
}

function eventTime(event: JsonObject): string {
  return (
    normalizeIsoTimestamp(event.written_at) ??
    normalizeIsoTimestamp(event.timestamp) ??
    normalizeIsoTimestamp(event.time) ??
    normalizeIsoTimestamp(event.created_at) ??
    nowIso()
  );
}

function normalizeIsoTimestamp(value: unknown): string | null {
  if (typeof value !== "string") return null;
  const parsed = Date.parse(value);
  return Number.isFinite(parsed) ? new Date(parsed).toISOString() : null;
}

function nowIso(): string {
  return new Date().toISOString();
}

function shortJson(value: unknown): string {
  if (value == null) return "";
  if (typeof value === "string") return value;
  try {
    return JSON.stringify(value, null, 2);
  } catch {
    return String(value);
  }
}

function stableStringHash(value: string): string {
  let hash = 2166136261;
  for (let i = 0; i < value.length; i += 1) {
    hash ^= value.charCodeAt(i);
    hash = Math.imul(hash, 16777619);
  }
  return (hash >>> 0).toString(36);
}
