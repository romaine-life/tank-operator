const STORAGE_KEY = "tank.sessionListDebug";
const MAX_EVENTS = 240;
const MAX_ROWS_PER_EVENT = 80;
const MAX_DETAIL_KEYS = 24;
const MAX_STRING_LENGTH = 280;

export type SessionListDebugRow = {
  id: string;
  name?: string | null;
  display_name?: string | null;
  pod_name?: string | null;
  mode?: string | null;
  status?: string | null;
  visible?: boolean | null;
  row_version?: number | null;
  sidebar_position?: number | null;
  agent_avatar_id?: string | null;
  system_avatar_id?: string | null;
  rendered_avatar_id?: string | null;
  rendered_avatar_src?: string | null;
  rendered_avatar_custom?: boolean | null;
  requested_at?: string | null;
  created_at?: string | null;
  ready_at?: string | null;
};

export type SessionListDebugEvent = {
  seq: number;
  at: string;
  kind: string;
  source?: string;
  reason?: string;
  session_id?: string;
  previous_active_id?: string | null;
  active_id?: string | null;
  cursor?: string | null;
  row?: SessionListDebugRow;
  rows?: SessionListDebugRow[];
  tombstones?: string[];
  detail?: unknown;
};

export type SessionListDebugSnapshot = {
  version: 1;
  seq: number;
  updated_at: string | null;
  location: string | null;
  store: {
    cursor: string | null;
    rows: SessionListDebugRow[];
    tombstones: string[];
    updated_at: string;
  } | null;
  render: {
    active_id: string | null;
    avatar_catalog_version?: number;
    sessions: SessionListDebugRow[];
    updated_at: string;
  } | null;
  events: SessionListDebugEvent[];
};

type Listener = () => void;

const listeners = new Set<Listener>();

let state: SessionListDebugSnapshot = readStoredSnapshot() ?? emptySnapshot();

export function summarizeSessionListRow(value: unknown): SessionListDebugRow | null {
  if (!isRecord(value)) return null;
  const id = stringField(value, "id");
  if (!id) return null;
  return {
    id,
    name: nullableStringField(value, "name"),
    display_name: nullableStringField(value, "display_name"),
    pod_name: nullableStringField(value, "pod_name"),
    mode: nullableStringField(value, "mode"),
    status: nullableStringField(value, "status"),
    visible: booleanField(value, "visible"),
    row_version: numberField(value, "row_version"),
    sidebar_position: numberField(value, "sidebar_position"),
    agent_avatar_id: nullableStringField(value, "agent_avatar_id"),
    system_avatar_id: nullableStringField(value, "system_avatar_id"),
    rendered_avatar_id: nullableStringField(value, "rendered_avatar_id"),
    rendered_avatar_src: nullableStringField(value, "rendered_avatar_src"),
    rendered_avatar_custom: booleanField(value, "rendered_avatar_custom"),
    requested_at: nullableStringField(value, "requested_at"),
    created_at: nullableStringField(value, "created_at"),
    ready_at: nullableStringField(value, "ready_at"),
  };
}

export function summarizeSessionListRows(values: readonly unknown[]): SessionListDebugRow[] {
  const rows: SessionListDebugRow[] = [];
  for (const value of values.slice(0, MAX_ROWS_PER_EVENT)) {
    const row = summarizeSessionListRow(value);
    if (row) rows.push(row);
  }
  return rows;
}

export function recordSessionListDebugEvent(
  input: Omit<SessionListDebugEvent, "seq" | "at">,
): void {
  const event: SessionListDebugEvent = {
    ...input,
    seq: state.seq + 1,
    at: nowISO(),
    row: input.row ? summarizeSessionListRow(input.row) ?? undefined : undefined,
    detail: compactValue(input.detail, 0),
    rows: input.rows ? summarizeSessionListRows(input.rows) : undefined,
    tombstones: input.tombstones?.slice(0, MAX_ROWS_PER_EVENT),
  };
  state = {
    ...state,
    seq: event.seq,
    updated_at: event.at,
    location: currentLocation(),
    events: [...state.events, event].slice(-MAX_EVENTS),
  };
  persistAndNotify();
}

export function updateSessionListDebugStore(args: {
  cursor: string | null;
  rows: readonly unknown[];
  tombstones: readonly string[];
}): void {
  state = {
    ...state,
    updated_at: nowISO(),
    location: currentLocation(),
    store: {
      cursor: args.cursor,
      rows: summarizeSessionListRows(args.rows),
      tombstones: [...args.tombstones],
      updated_at: nowISO(),
    },
  };
  persistAndNotify();
}

export function updateSessionListDebugRender(args: {
  active_id: string | null;
  avatar_catalog_version?: number;
  sessions: readonly unknown[];
}): void {
  const rows = summarizeSessionListRows(args.sessions);
  state = {
    ...state,
    updated_at: nowISO(),
    location: currentLocation(),
    render: {
      active_id: args.active_id,
      avatar_catalog_version: args.avatar_catalog_version,
      sessions: rows,
      updated_at: nowISO(),
    },
  };
  recordSessionListDebugEvent({
    kind: "render-state",
    source: "App",
    active_id: args.active_id,
    rows,
    detail: { avatar_catalog_version: args.avatar_catalog_version },
  });
}

export function getSessionListDebugSnapshot(): SessionListDebugSnapshot {
  return {
    ...state,
    store: state.store
      ? { ...state.store, rows: [...state.store.rows], tombstones: [...state.store.tombstones] }
      : null,
    render: state.render
      ? { ...state.render, sessions: [...state.render.sessions] }
      : null,
    events: [...state.events],
  };
}

export function subscribeSessionListDebug(listener: Listener): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

export function resetSessionListDebugForTest(): void {
  state = emptySnapshot();
  persistAndNotify();
}

function emptySnapshot(): SessionListDebugSnapshot {
  return {
    version: 1,
    seq: 0,
    updated_at: null,
    location: currentLocation(),
    store: null,
    render: null,
    events: [],
  };
}

function persistAndNotify(): void {
  try {
    sessionStorage.setItem(STORAGE_KEY, JSON.stringify(state));
  } catch {
    // Debug state must never affect product behavior.
  }
  exposeSnapshot();
  for (const listener of listeners) listener();
}

function exposeSnapshot(): void {
  if (typeof window === "undefined") return;
  window.__tankSessionListDebug = getSessionListDebugSnapshot;
}

function readStoredSnapshot(): SessionListDebugSnapshot | null {
  try {
    const raw = sessionStorage.getItem(STORAGE_KEY);
    if (!raw) return null;
    const parsed = JSON.parse(raw) as Partial<SessionListDebugSnapshot>;
    if (parsed.version !== 1 || !Array.isArray(parsed.events)) return null;
    return {
      version: 1,
      seq: typeof parsed.seq === "number" ? parsed.seq : 0,
      updated_at: typeof parsed.updated_at === "string" ? parsed.updated_at : null,
      location: typeof parsed.location === "string" ? parsed.location : currentLocation(),
      store: parsed.store ?? null,
      render: parsed.render ?? null,
      events: parsed.events.slice(-MAX_EVENTS) as SessionListDebugEvent[],
    };
  } catch {
    return null;
  }
}

function nowISO(): string {
  return new Date().toISOString();
}

function currentLocation(): string | null {
  if (typeof window === "undefined") return null;
  return `${window.location.pathname}${window.location.search}`;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function stringField(value: Record<string, unknown>, key: string): string | null {
  const field = value[key];
  return typeof field === "string" && field !== "" ? field : null;
}

function nullableStringField(value: Record<string, unknown>, key: string): string | null {
  const field = value[key];
  return typeof field === "string" ? truncateString(field) : null;
}

function numberField(value: Record<string, unknown>, key: string): number | null {
  const field = value[key];
  if (typeof field === "number" && Number.isFinite(field)) return field;
  if (typeof field === "string" && field !== "") {
    const parsed = Number(field);
    if (Number.isFinite(parsed)) return parsed;
  }
  return null;
}

function booleanField(value: Record<string, unknown>, key: string): boolean | null {
  const field = value[key];
  return typeof field === "boolean" ? field : null;
}

function truncateString(value: string): string {
  if (value.length <= MAX_STRING_LENGTH) return value;
  return `${value.slice(0, MAX_STRING_LENGTH - 3)}...`;
}

function compactValue(value: unknown, depth: number): unknown {
  if (value == null || typeof value === "number" || typeof value === "boolean") return value;
  if (typeof value === "string") return truncateString(value);
  if (depth >= 3) return "[truncated]";
  if (Array.isArray(value)) return value.slice(0, MAX_DETAIL_KEYS).map((item) => compactValue(item, depth + 1));
  if (!isRecord(value)) return String(value);
  const out: Record<string, unknown> = {};
  for (const key of Object.keys(value).slice(0, MAX_DETAIL_KEYS)) {
    out[key] = compactValue(value[key], depth + 1);
  }
  return out;
}

declare global {
  interface Window {
    __tankSessionListDebug?: () => SessionListDebugSnapshot;
  }
}

exposeSnapshot();
