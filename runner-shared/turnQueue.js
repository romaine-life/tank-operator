import { randomUUID } from "node:crypto";
export const TURN_QUEUE_LEASE_MS = parsePositiveInt(process.env.TURN_QUEUE_LEASE_MS, 120_000);
export const TURN_QUEUE_MAX_ATTEMPTS = parsePositiveInt(process.env.TURN_QUEUE_MAX_ATTEMPTS, 3);
const TURN_QUEUE_RENEW_MS = Math.max(1_000, Math.floor(TURN_QUEUE_LEASE_MS / 3));
export class SharedTurnQueue {
    cfg;
    provider;
    client;
    container;
    runnerID;
    constructor(cfg, provider, deps) {
        this.cfg = cfg;
        this.provider = provider;
        this.client = new deps.CosmosClient({
            endpoint: cfg.cosmosEndpoint,
            aadCredentials: new deps.DefaultAzureCredential(),
        });
        this.container = this.client
            .database(cfg.cosmosDatabase)
            .container(cfg.turnQueueContainer);
        this.runnerID = `${provider}-runner:${cfg.sessionId}:${randomUUID()}`;
    }
    async claimNext() {
        const now = new Date();
        const nowISO = now.toISOString();
        const iterator = this.container.items.query({
            query: "SELECT TOP 10 * FROM c WHERE c.session_id = @session_id AND c.provider = @provider AND (c.source = @source_sdk OR c.source = @source_wakeup) AND (c.status = @status_pending OR (c.status = @status_claimed AND IS_DEFINED(c.claim_expires_at) AND c.claim_expires_at <= @now)) AND (NOT IS_DEFINED(c.available_at) OR IS_NULL(c.available_at) OR c.available_at <= @now) ORDER BY c.created_at ASC",
            parameters: [
                { name: "@session_id", value: this.cfg.sessionId },
                { name: "@provider", value: this.provider },
                { name: "@source_sdk", value: "sdk" },
                { name: "@source_wakeup", value: "schedule-wakeup" },
                { name: "@status_pending", value: "pending" },
                { name: "@status_claimed", value: "claimed" },
                { name: "@now", value: nowISO },
            ],
        }, { partitionKey: this.cfg.sessionId });
        const page = await iterator.fetchNext();
        for (const record of page.resources) {
            const claimed = await this.claim(record, now);
            if (claimed)
                return claimed;
        }
        return null;
    }
    async enqueueDelayed(args) {
        const record = buildDelayedTurnRecord({
            sessionID: this.cfg.sessionId,
            email: this.cfg.ownerEmail,
            provider: this.provider,
            prompt: args.prompt,
            clientNonce: args.clientNonce,
            availableAt: args.availableAt,
            now: new Date(),
        });
        try {
            await this.container.items.create(record);
        }
        catch (err) {
            if (!isConflict(err))
                throw err;
        }
        return record;
    }
    async markCompleted(record) {
        return this.markStatus(record, "completed");
    }
    async markFailed(record, err) {
        return this.markStatus(record, "failed", errorText(err));
    }
    async renewLease(record) {
        const item = this.container.item(record.id, record.session_id);
        const response = await item.read();
        const current = response.resource;
        if (!current || isTerminalStatus(current.status))
            return false;
        if (!claimMatches(current, record))
            return false;
        const renewed = {
            ...current,
            claim_expires_at: new Date(Date.now() + TURN_QUEUE_LEASE_MS).toISOString(),
        };
        try {
            const replace = await item.replace(renewed, etagOptions(current));
            if (replace.resource)
                Object.assign(record, replace.resource);
            return true;
        }
        catch (err) {
            if (isClaimRace(err))
                return false;
            throw err;
        }
    }
    startLeaseRenewal(record) {
        let stopped = false;
        const timer = setInterval(() => {
            if (stopped)
                return;
            void this.renewLease(record).catch((err) => console.error("turn queue lease renewal failed:", err));
        }, TURN_QUEUE_RENEW_MS);
        return () => {
            stopped = true;
            clearInterval(timer);
        };
    }
    attemptsExceeded(record) {
        return claimAttemptsExceeded(record);
    }
    async claim(record, now) {
        const claimed = buildClaimedRecord(record, {
            claimID: randomUUID(),
            claimedBy: this.runnerID,
            now,
            leaseMs: TURN_QUEUE_LEASE_MS,
        });
        try {
            const response = await this.container
                .item(record.id, record.session_id)
                .replace(claimed, etagOptions(record));
            return response.resource ?? claimed;
        }
        catch (err) {
            if (isClaimRace(err))
                return null;
            throw err;
        }
    }
    async markStatus(record, status, lastError) {
        const item = this.container.item(record.id, record.session_id);
        const response = await item.read();
        const current = response.resource;
        if (!current)
            return false;
        if (isTerminalStatus(current.status))
            return true;
        if (!claimMatches(current, record))
            return false;
        const now = new Date().toISOString();
        try {
            await item.replace({
                ...current,
                status,
                completed_at: now,
                claim_expires_at: null,
                ...(lastError ? { last_error: lastError } : {}),
            }, etagOptions(current));
            return true;
        }
        catch (err) {
            if (isClaimRace(err))
                return false;
            throw err;
        }
    }
}
export function buildClaimedRecord(record, args) {
    return {
        ...record,
        status: "claimed",
        claimed_at: args.now.toISOString(),
        claim_id: args.claimID,
        claimed_by: args.claimedBy,
        claim_expires_at: new Date(args.now.getTime() + args.leaseMs).toISOString(),
        attempt_count: attemptCount(record) + 1,
    };
}
export function buildDelayedTurnRecord(args) {
    return {
        id: `turn:${args.clientNonce}`,
        run_id: args.clientNonce,
        session_id: args.sessionID,
        email: args.email,
        provider: args.provider,
        source: "schedule-wakeup",
        client_nonce: args.clientNonce,
        prompt: args.prompt,
        status: "pending",
        created_at: args.now.toISOString(),
        available_at: args.availableAt,
        claimed_at: null,
        claim_id: null,
        claimed_by: null,
        claim_expires_at: null,
        attempt_count: 0,
        completed_at: null,
    };
}
export function claimAttemptsExceeded(record, maxAttempts = TURN_QUEUE_MAX_ATTEMPTS) {
    return attemptCount(record) > maxAttempts;
}
function etagOptions(record) {
    return record._etag
        ? { accessCondition: { type: "IfMatch", condition: record._etag } }
        : undefined;
}
function attemptCount(record) {
    return typeof record.attempt_count === "number" && Number.isFinite(record.attempt_count)
        ? record.attempt_count
        : 0;
}
function claimMatches(current, claimant) {
    if (!current.claim_id)
        return true;
    return typeof claimant.claim_id === "string" && claimant.claim_id === current.claim_id;
}
function isTerminalStatus(status) {
    return status === "completed" || status === "failed";
}
function isClaimRace(err) {
    if (!err || typeof err !== "object")
        return false;
    const statusCode = err.statusCode;
    const code = err.code;
    return statusCode === 409 || statusCode === 412 || code === 409 || code === 412;
}
function isConflict(err) {
    if (!err || typeof err !== "object")
        return false;
    const statusCode = err.statusCode;
    const code = err.code;
    return statusCode === 409 || code === 409 || code === "Conflict";
}
function errorText(err) {
    if (err instanceof Error)
        return err.message;
    return String(err);
}
function parsePositiveInt(value, fallback) {
    const parsed = parseInt(value?.trim() || "", 10);
    return Number.isFinite(parsed) && parsed > 0 ? parsed : fallback;
}
export function turnClientNonce(record) {
    return record.client_nonce?.trim() || record.run_id;
}
