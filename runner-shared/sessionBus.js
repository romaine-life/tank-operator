import { createHash, randomUUID } from "node:crypto";
import { readFile } from "node:fs/promises";

export const SESSION_COMMAND_ACK_MS = parsePositiveInt(process.env.SESSION_COMMAND_ACK_MS, 120_000);
export const SESSION_COMMAND_MAX_DELIVER = parsePositiveInt(process.env.SESSION_COMMAND_MAX_DELIVER, 20);
const SESSION_COMMAND_WORKING_MS = Math.max(1_000, Math.floor(SESSION_COMMAND_ACK_MS / 3));

// Control-plane consumer config: tuned for low-latency delivery of
// interrupts during an in-flight turn. ACK window is short (the handler
// either completes synchronously or NAKs); max in-flight is sized for
// burst clicks (double-clicked Stop, queued retries) without being so
// large that an orchestrator bug could fan-spam the runner.
//
// NOT shared with the data-plane consumer constants above on purpose:
// the data plane wants long ack windows (turn duration) and serial
// dispatch (max_ack_pending=1); the control plane wants the opposite.
// If you find yourself unifying them, re-read
// docs/tank-conversation-protocol.md → "Durable turn interruption".
export const SESSION_CONTROL_ACK_MS = parsePositiveInt(process.env.SESSION_CONTROL_ACK_MS, 15_000);
export const SESSION_CONTROL_MAX_DELIVER = parsePositiveInt(process.env.SESSION_CONTROL_MAX_DELIVER, 10);
export const SESSION_CONTROL_MAX_ACK_PENDING = parsePositiveInt(
    process.env.SESSION_CONTROL_MAX_ACK_PENDING,
    16,
);

export class SharedSessionBus {
    cfg;
    provider;
    deps;
    sessionStorageKey;
    stream;
    nc = null;
    js = null;
    jsm = null;
    runnerID;
    constructor(cfg, provider, deps) {
        this.cfg = cfg;
        this.provider = provider;
        this.deps = deps;
        this.sessionStorageKey = cfg.sessionStorageKey || cfg.sessionId;
        this.stream = cfg.natsStream || "TANK_SESSION_BUS";
        this.runnerID = `${provider}-runner:${this.sessionStorageKey}:${randomUUID()}`;
    }
    async startCommandConsumer(handler, signal) {
        await this.ensureConnected();
        await this.ensureConsumer();
        const consumer = await this.js.consumers.get(this.stream, this.consumerName());
        const messages = await consumer.consume({
            max_messages: 10,
            threshold_messages: 5,
            expires: 30_000,
            idle_heartbeat: 5_000,
        });
        let stopped = false;
        const stop = async () => {
            stopped = true;
            await messages.close();
        };
        signal?.addEventListener("abort", () => {
            void stop();
        }, { once: true });
        void (async () => {
            for await (const msg of messages) {
                if (stopped || signal?.aborted) break;
                const command = this.commandFromMessage(msg);
                const record = new SessionCommandRecord(command, msg);
                // Cutover hygiene: interrupts and input_reply are
                // control-plane and MUST arrive on the control consumer
                // (see startControlConsumer). A stray control command on
                // the data-plane subject is either a pre-cutover straggler
                // in the JetStream replay buffer or a backend regression.
                // Ack-and-drop with a structured warn so the message
                // doesn't block the serial submit_turn consumer
                // (max_ack_pending=1) and the regression is visible in
                // logs. This is NOT a fallback path — the data-plane
                // handler is never invoked for these; the control plane
                // is the only place they can take effect.
                if (isInterruptCommand(command) || isInputReplyCommand(command)) {
                    console.warn("session bus: dropped stray control command on data plane (control plane is the supported path)", {
                        type: command.type,
                        command_id: command.command_id,
                        target_turn_id: command.target_turn_id,
                    });
                    record.ack();
                    continue;
                }
                try {
                    await handler(record);
                }
                catch (err) {
                    console.error("session bus command handler failed:", err);
                    record.nak(5_000);
                }
            }
        })().catch((err) => console.error("session bus command consumer crashed:", err));
        return stop;
    }
    // startControlConsumer subscribes to the control-plane subject (today:
    // interrupt_turn; future: any low-latency control signal). Sibling of
    // startCommandConsumer with three deliberate differences:
    //
    //   1. Different filter_subject (controlSubject vs commandSubject) so a
    //      JetStream max_ack_pending budget on the data plane can never hold
    //      a control command behind an in-flight submit_turn — that was the
    //      "Stop doesn't interrupt deep tool-use loops" regression.
    //   2. max_ack_pending sized for control burst (default 16) rather than
    //      serial dispatch (1 on the data plane).
    //   3. Shorter ack_wait — control handlers complete synchronously
    //      (dispatch to sdkQuery.interrupt() or codex AbortController) and
    //      either ack or NAK quickly; no working() heartbeat needed.
    //
    // Durable consumer name is provider-scoped per session so a runner-
    // process restart re-attaches and any unacked control command replays.
    async startControlConsumer(handler, signal) {
        await this.ensureConnected();
        await this.ensureControlConsumer();
        const consumer = await this.js.consumers.get(this.stream, this.controlConsumerName());
        const messages = await consumer.consume({
            max_messages: 16,
            threshold_messages: 8,
            expires: 30_000,
            idle_heartbeat: 5_000,
        });
        let stopped = false;
        const stop = async () => {
            stopped = true;
            await messages.close();
        };
        signal?.addEventListener("abort", () => {
            void stop();
        }, { once: true });
        void (async () => {
            for await (const msg of messages) {
                if (stopped || signal?.aborted) break;
                const command = this.commandFromMessage(msg);
                const record = new SessionCommandRecord(command, msg);
                try {
                    await handler(record);
                }
                catch (err) {
                    console.error("session bus control handler failed:", err);
                    record.nak(2_000);
                }
            }
        })().catch((err) => console.error("session bus control consumer crashed:", err));
        return stop;
    }
    async publishEvent(event, options = {}) {
        await this.ensureConnected();
        const doc = this.eventDoc(event);
        // Defensive: the runner-side dispatch wrapper truncates oversized
        // events before they reach here (see truncateEventIfOversized
        // below and nelsong6/tank-operator#532 Stage 3 for the contract).
        // This belt-and-braces check guarantees no wire publish exceeds
        // the transport budget even if a future code path bypasses the
        // dispatch wrapper. NATS's default max_payload is 1 MiB; the
        // 900 KiB threshold leaves headroom for JetStream/protocol
        // framing.
        const safe = truncateEventIfOversized(doc).event;
        const ack = await this.js.publish(eventSubject(this.sessionStorageKey), encodeJSON(safe), {
            msgID: safe.id,
        });
        return ack.duplicate ? "exists" : "created";
    }
    async enqueueWakeupSubmitTurn(args) {
        await this.ensureConnected();
        const command = buildWakeupSubmitTurnCommand({
            sessionID: this.cfg.sessionId,
            sessionStorageKey: this.sessionStorageKey,
            email: this.cfg.ownerEmail,
            provider: this.provider,
            prompt: args.prompt,
            clientNonce: args.clientNonce,
        });
        await this.js.publish(commandSubject(this.sessionStorageKey, this.provider), encodeJSON(command), {
            msgID: command.command_id,
        });
        return command;
    }
    async findTurnTerminal(turnID) {
        const baseURL = trimTrailingSlashes(this.cfg.operatorInternalURL || "");
        const tokenPath = this.cfg.operatorTokenPath || "";
        if (!baseURL || !tokenPath || !turnID) return null;
        const token = (await readFile(tokenPath, "utf8")).trim();
        const url = `${baseURL}/api/internal/sessions/${encodeURIComponent(this.cfg.sessionId)}/turns/${encodeURIComponent(turnID)}/terminal`;
        const response = await fetch(url, {
            headers: { Authorization: `Bearer ${token}` },
        });
        if (!response.ok) {
            throw new Error(`terminal check failed: ${response.status}`);
        }
        const body = await response.json();
        return body?.terminal ? body.event ?? null : null;
    }
    markCompleted(record) {
        record.ack();
        return Promise.resolve(true);
    }
    markFailed(record, err) {
        const attempts = deliveryCount(record);
        if (attempts >= SESSION_COMMAND_MAX_DELIVER) {
            record.term(errorText(err));
        }
        else {
            record.nak(5_000);
        }
        return Promise.resolve(true);
    }
    startWorkHeartbeat(record) {
        let stopped = false;
        const timer = setInterval(() => {
            if (!stopped) record.working();
        }, SESSION_COMMAND_WORKING_MS);
        return () => {
            stopped = true;
            clearInterval(timer);
        };
    }
    attemptsExceeded(record) {
        return deliveryCount(record) > SESSION_COMMAND_MAX_DELIVER;
    }
    async close() {
        await this.nc?.drain();
        this.nc = null;
        this.js = null;
        this.jsm = null;
    }
    async ensureConnected() {
        if (this.nc && this.js && this.jsm) return;
        const servers = this.cfg.natsURL || process.env.NATS_URL;
        if (!servers) throw new Error("NATS_URL is required");
        this.nc = await this.deps.connect({
            servers,
            token: this.cfg.natsToken || process.env.NATS_TOKEN,
            name: this.runnerID,
        });
        this.js = this.deps.jetstream(this.nc);
        this.jsm = await this.deps.jetstreamManager(this.nc, { checkAPI: true });
    }
    async ensureConsumer() {
        const name = this.consumerName();
        const cfg = {
            durable_name: name,
            name,
            description: `${this.provider} session command consumer`,
            filter_subject: commandSubject(this.sessionStorageKey, this.provider),
            ack_policy: this.deps.AckPolicy.Explicit,
            deliver_policy: this.deps.DeliverPolicy.All,
            replay_policy: this.deps.ReplayPolicy.Instant,
            ack_wait: this.deps.nanos(SESSION_COMMAND_ACK_MS),
            max_deliver: SESSION_COMMAND_MAX_DELIVER,
            max_ack_pending: 1,
            inactive_threshold: this.deps.nanos(7 * 24 * 60 * 60 * 1000),
        };
        try {
            await this.jsm.consumers.add(this.stream, cfg);
        }
        catch (err) {
            try {
                await this.jsm.consumers.info(this.stream, name);
            }
            catch {
                throw err;
            }
            await this.jsm.consumers.update(this.stream, name, {
                ack_wait: cfg.ack_wait,
                max_deliver: cfg.max_deliver,
                max_ack_pending: cfg.max_ack_pending,
                inactive_threshold: cfg.inactive_threshold,
            });
        }
    }
    // ensureControlConsumer is the sibling of ensureConsumer for the
    // control-plane subject. The two consumers MUST stay distinct (separate
    // durable name, separate filter_subject) so an in-flight data-plane
    // command's ack window can never gate control delivery.
    async ensureControlConsumer() {
        const name = this.controlConsumerName();
        const cfg = {
            durable_name: name,
            name,
            description: `${this.provider} session control consumer`,
            filter_subject: controlSubject(this.sessionStorageKey, this.provider),
            ack_policy: this.deps.AckPolicy.Explicit,
            deliver_policy: this.deps.DeliverPolicy.All,
            replay_policy: this.deps.ReplayPolicy.Instant,
            ack_wait: this.deps.nanos(SESSION_CONTROL_ACK_MS),
            max_deliver: SESSION_CONTROL_MAX_DELIVER,
            max_ack_pending: SESSION_CONTROL_MAX_ACK_PENDING,
            inactive_threshold: this.deps.nanos(7 * 24 * 60 * 60 * 1000),
        };
        try {
            await this.jsm.consumers.add(this.stream, cfg);
        }
        catch (err) {
            try {
                await this.jsm.consumers.info(this.stream, name);
            }
            catch {
                throw err;
            }
            await this.jsm.consumers.update(this.stream, name, {
                ack_wait: cfg.ack_wait,
                max_deliver: cfg.max_deliver,
                max_ack_pending: cfg.max_ack_pending,
                inactive_threshold: cfg.inactive_threshold,
            });
        }
    }
    consumerName() {
        return `${sanitizeConsumerName(this.provider)}_${storageToken(this.sessionStorageKey)}`;
    }
    controlConsumerName() {
        return `${sanitizeConsumerName(this.provider)}_control_${storageToken(this.sessionStorageKey)}`;
    }
    commandFromMessage(msg) {
        const command = msg.json();
        return {
            ...command,
            id: command.command_id || `command:${msg.seq}`,
            source: command.source || command.type,
            status: "claimed",
            attempt_count: deliveryCount({ message: msg }),
        };
    }
    eventDoc(event) {
        const id = String(event.uuid || event.event_id || randomUUID());
        return {
            ...event,
            uuid: id,
            id,
            tank_session_id: this.sessionStorageKey,
            tank_public_session_id: this.cfg.sessionId,
            email: this.cfg.ownerEmail,
            runtime: this.provider,
            written_at: typeof event.written_at === "string" ? event.written_at : new Date().toISOString(),
        };
    }
}

export class SessionCommandRecord {
    constructor(command, message) {
        Object.assign(this, command);
        this.message = message;
    }
    ack() {
        this.message.ack();
    }
    nak(delayMs) {
        this.message.nak(delayMs);
    }
    term(reason) {
        this.message.term(reason);
    }
    working() {
        this.message.working();
    }
}

export function buildWakeupSubmitTurnCommand(args) {
    const now = new Date().toISOString();
    return {
        schema_version: 1,
        command_id: `turn:${args.clientNonce}`,
        type: "submit_turn",
        session_id: args.sessionID,
        session_storage_key: args.sessionStorageKey || args.sessionID,
        email: args.email,
        provider: args.provider,
        source: "schedule-wakeup",
        turn_id: turnIDForClientNonce(args.clientNonce),
        client_nonce: args.clientNonce,
        prompt: args.prompt,
        created_at: now,
    };
}

export function isInterruptCommand(record) {
    return record?.type === "interrupt_turn" || record?.source === "interrupt";
}

export function isInputReplyCommand(record) {
    return record?.type === "input_reply" || record?.source === "input-reply";
}

export function commandClientNonce(record) {
    return record.client_nonce?.trim() || record.turn_id;
}

export function turnIDForClientNonce(clientNonce) {
    return `turn_${stableIDPart(clientNonce)}`;
}

function commandSubject(sessionStorageKey, provider) {
    return `tank.session.${storageToken(sessionStorageKey)}.commands.${sanitizeSubjectToken(provider)}`;
}

// controlSubject mirrors backend-go's sessionbus.ControlSubject. The two
// helpers MUST stay in lockstep — if the wire shape diverges, the runner
// won't see interrupts. See scripts/check-stop-request-migration.mjs for
// the regression guard that grep-pins both sides.
export function controlSubject(sessionStorageKey, provider) {
    return `tank.session.${storageToken(sessionStorageKey)}.control.${sanitizeSubjectToken(provider)}`;
}

function eventSubject(sessionStorageKey) {
    return `tank.session.${storageToken(sessionStorageKey)}.events`;
}

function storageToken(value) {
    return Buffer.from(String(value || "").trim(), "utf8").toString("base64url");
}

function sanitizeSubjectToken(value) {
    return String(value || "").trim().toLowerCase().replace(/[^a-z0-9_-]/g, "_") || "_";
}

function sanitizeConsumerName(provider) {
    return sanitizeSubjectToken(provider).replace(/-/g, "_");
}

function stableIDPart(value) {
    const safe = String(value || "")
        .trim()
        .replace(/[^A-Za-z0-9_.:-]+/g, "-")
        .replace(/-+/g, "-")
        .replace(/^-|-$/g, "");
    const hash = createHashPart(value);
    if (safe.length >= 6 && safe.length <= 80) return safe;
    if (safe.length > 80) return `${safe.slice(0, 64)}-${hash}`;
    return hash;
}

function createHashPart(value) {
    return createHash("sha256").update(String(value)).digest("hex").slice(0, 12);
}

function encodeJSON(value) {
    return new TextEncoder().encode(JSON.stringify(value));
}

function deliveryCount(record) {
    const message = record?.message;
    const count = message?.info?.deliveryCount ?? message?.info?.redeliveryCount ?? record?.attempt_count;
    return typeof count === "number" && Number.isFinite(count) ? count : 1;
}

function trimTrailingSlashes(value) {
    return String(value || "").replace(/\/+$/, "");
}

function errorText(err) {
    if (err instanceof Error) return err.message;
    return String(err);
}

function parsePositiveInt(value, fallback) {
    const parsed = parseInt(value?.trim() || "", 10);
    return Number.isFinite(parsed) && parsed > 0 ? parsed : fallback;
}

// ─────────────────────────────────────────────────────────────────────────────
// Oversized-event truncation (PR 3 of nelsong6/tank-operator#532)
//
// NATS clients throw `InvalidArgumentError: 'payload' max_payload size
// exceeded` synchronously when an encoded message exceeds the server's
// max_payload (1 MiB by default). The throw is deterministic; retries
// don't help. Pre-#532 a Tank conversation event whose payload exceeded
// the limit (typically a tool_result.output from Read of a large file
// or Bash with a long output) silently went into the void: dispatch()
// caught the throw and the runner moved on, leaving a hole in the
// durable ledger and triggering downstream symptoms (UI stuck because a
// turn.interrupted event for a different turn also got dropped, etc.).
//
// Session 19's evidence in #532: 7 publish failures total across the
// pod's lifetime, each killing one event. Small in absolute count but
// each one is a hole. After PR 1 (#535) the stop control became self-
// telling even when publishes fail — but the underlying transport-layer
// issue stayed. This stage's contract: NO Tank event payload reaches the
// wire with body > maxBytes. Oversized strings are replaced with a
// typed marker that names the original size and a SHA256 prefix, so the
// durable record's shape is preserved and the user-visible transcript
// degrades gracefully ("[…N bytes truncated]") rather than vanishing.
//
// The output shape is intentionally a STRING (not a structured object)
// because callers downstream — adapters, the persister, the frontend
// renderer — read these fields as strings. Replacing a string with an
// object would shift the schema; the marker keeps the shape stable and
// is human-readable on inspection.
// ─────────────────────────────────────────────────────────────────────────────

const DEFAULT_MAX_EVENT_BYTES = parsePositiveInt(
    process.env.SESSION_EVENT_MAX_BYTES,
    900_000,
);
const DEFAULT_STRING_TRUNCATE_THRESHOLD = parsePositiveInt(
    process.env.SESSION_EVENT_STRING_THRESHOLD,
    50_000,
);

export function truncateEventIfOversized(event, options = {}) {
    const maxBytes = options.maxBytes ?? DEFAULT_MAX_EVENT_BYTES;
    const stringThreshold = options.stringThreshold ?? DEFAULT_STRING_TRUNCATE_THRESHOLD;
    const initialBytes = encodeJSON(event).length;
    if (initialBytes <= maxBytes) {
        return { event, truncated: false, originalBytes: initialBytes, finalBytes: initialBytes, fields: [] };
    }
    // Deep clone so we don't mutate the caller's stamped event.
    const working = JSON.parse(JSON.stringify(event));
    const fields = [];
    truncateLargeStrings(working, stringThreshold, fields, [""]);
    let bytes = encodeJSON(working).length;
    // Aggressive pass: lower the per-string threshold until we fit or we
    // run out of strings worth cutting. Geometric reduction with a 1 KiB
    // floor so we don't churn forever on a payload made of many small
    // strings.
    let aggressive = Math.max(1024, Math.floor(stringThreshold / 4));
    while (bytes > maxBytes && aggressive >= 1024) {
        const before = fields.length;
        truncateLargeStrings(working, aggressive, fields, [""]);
        bytes = encodeJSON(working).length;
        if (fields.length === before) break; // nothing more to cut at this threshold
        aggressive = Math.floor(aggressive / 2);
    }
    if (bytes <= maxBytes) {
        return {
            event: working,
            truncated: true,
            originalBytes: initialBytes,
            finalBytes: bytes,
            fields,
            reason: "strings-truncated",
        };
    }
    // Last resort: drop the payload entirely so the durable ledger still
    // gets a record that "an event existed here" with its envelope
    // (type, turn_id, event_id, etc.) intact. The persister-side
    // ValidateEventMap accepts a payload of any shape; the SPA renderer
    // can show the marker as a degraded item.
    working.payload = {
        __payload_dropped: true,
        original_bytes: initialBytes,
        reason: "event_oversized_after_truncation",
    };
    bytes = encodeJSON(working).length;
    return {
        event: working,
        truncated: true,
        originalBytes: initialBytes,
        finalBytes: bytes,
        fields,
        reason: "payload-dropped",
        payloadDropped: true,
    };
}

function truncateLargeStrings(node, threshold, accumulator, pathParts) {
    if (!node || typeof node !== "object") return;
    if (Array.isArray(node)) {
        for (let i = 0; i < node.length; i++) {
            const v = node[i];
            if (typeof v === "string" && v.length > threshold) {
                accumulator.push({
                    path: `${pathParts.join(".")}[${i}]`,
                    original_bytes: v.length,
                });
                node[i] = truncationMarker(v);
            } else if (v && typeof v === "object") {
                pathParts.push(`[${i}]`);
                truncateLargeStrings(v, threshold, accumulator, pathParts);
                pathParts.pop();
            }
        }
        return;
    }
    for (const [key, value] of Object.entries(node)) {
        if (typeof value === "string" && value.length > threshold) {
            accumulator.push({
                path: pathParts.join(".") + (pathParts.length > 0 ? "." : "") + key,
                original_bytes: value.length,
            });
            node[key] = truncationMarker(value);
        } else if (value && typeof value === "object") {
            pathParts.push(key);
            truncateLargeStrings(value, threshold, accumulator, pathParts);
            pathParts.pop();
        }
    }
}

function truncationMarker(value) {
    const hash = createHash("sha256").update(value, "utf8").digest("hex").slice(0, 16);
    return `[truncated: ${value.length} bytes original; sha256_16=${hash}; reason=event-too-large for transport]`;
}
