import { createHash, randomUUID } from "node:crypto";
import { readFile } from "node:fs/promises";

export const SESSION_COMMAND_ACK_MS = parsePositiveInt(process.env.SESSION_COMMAND_ACK_MS, 120_000);
export const SESSION_COMMAND_MAX_DELIVER = parsePositiveInt(process.env.SESSION_COMMAND_MAX_DELIVER, 20);
const SESSION_COMMAND_WORKING_MS = Math.max(1_000, Math.floor(SESSION_COMMAND_ACK_MS / 3));

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
    async publishEvent(event, options = {}) {
        await this.ensureConnected();
        const doc = this.eventDoc(event);
        const ack = await this.js.publish(eventSubject(this.sessionStorageKey), encodeJSON(doc), {
            msgID: doc.id,
        });
        return ack.duplicate ? "exists" : "created";
    }
    async enqueueWakeupCommand(args) {
        await this.ensureConnected();
        const command = buildScheduleWakeupCommand({
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
    consumerName() {
        return `${sanitizeConsumerName(this.provider)}_${storageToken(this.sessionStorageKey)}`;
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

export function buildScheduleWakeupCommand(args) {
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
