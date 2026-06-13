// Supervised session-bus contract (issue #1076 item 1). The previous shape
// made every runner mortal: the NATS client defaulted to 10 reconnect
// attempts, consumers were started exactly once at boot, and any iterator
// crash was only logged — a NATS disruption longer than ~25s left a
// deaf-but-alive zombie process holding the session. These tests pin the
// replacement: unlimited reconnects, supervised consumer restarts with
// durable re-ensure, loud process-exit on PERMANENT connection loss (and
// silence on our own graceful close), and the /healthz liveness signal.

import { strict as assert } from "node:assert";
import { mkdtemp, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";

import { SharedSessionBus } from "../../runner-shared/sessionBus.js";

type AnyRecord = Record<string, unknown>;

function fakeMessage(commandId: string): AnyRecord {
  return {
    seq: 1,
    info: { deliveryCount: 1 },
    json: () => ({ command_id: commandId, type: "submit_turn", client_nonce: commandId }),
    ack: () => {},
    nak: (_delay?: number) => {},
    term: (_reason?: string) => {},
    working: () => {},
  };
}

// oneShotIterator yields the given messages then ends (the iterator-death
// shape: heartbeats missed, consumer deleted, JetStream restarted).
// blockingIterator yields its messages then parks until close().
function oneShotIterator(messages: AnyRecord[]) {
  return {
    async close() {},
    async *[Symbol.asyncIterator]() {
      for (const m of messages) yield m;
    },
  };
}

function blockingIterator(messages: AnyRecord[]) {
  let release: (() => void) | null = null;
  const parked = new Promise<void>((resolve) => {
    release = resolve;
  });
  return {
    async close() {
      release?.();
    },
    async *[Symbol.asyncIterator]() {
      for (const m of messages) yield m;
      await parked;
    },
  };
}

interface FakeDepsOptions {
  iterators: Array<ReturnType<typeof oneShotIterator>>;
  onConsumerRestart?: (kind: string) => void;
  onFatalConnectionLoss?: (err: Error | null) => void;
  onConnectionStatus?: (type: string) => void;
}

function fakeDeps(opts: FakeDepsOptions) {
  const state = {
    connectOptions: null as AnyRecord | null,
    consumerGets: 0,
    ensureAdds: 0,
    closedResolve: null as ((err: Error | null) => void) | null,
    isClosed: false,
  };
  const nc = {
    status: () =>
      ({
        async *[Symbol.asyncIterator]() {
          await new Promise(() => {}); // never emits; never ends
        },
      }) as AsyncIterable<unknown>,
    closed: () =>
      new Promise<Error | null>((resolve) => {
        state.closedResolve = resolve;
      }),
    isClosed: () => state.isClosed,
    close: async () => {
      state.isClosed = true;
      state.closedResolve?.(null);
    },
  };
  const deps = {
    connect: async (options: AnyRecord) => {
      state.connectOptions = options;
      return nc;
    },
    jetstream: () => ({
      consumers: {
        get: async () => {
          const iterator = opts.iterators[Math.min(state.consumerGets, opts.iterators.length - 1)];
          state.consumerGets += 1;
          return { consume: async () => iterator };
        },
      },
    }),
    jetstreamManager: async () => ({
      consumers: {
        add: async () => {
          state.ensureAdds += 1;
        },
        info: async () => ({}),
        update: async () => ({}),
      },
    }),
    AckPolicy: { Explicit: "explicit" },
    DeliverPolicy: { All: "all" },
    ReplayPolicy: { Instant: "instant" },
    nanos: (ms: number) => ms * 1_000_000,
    onConsumerRestart: opts.onConsumerRestart,
    onFatalConnectionLoss: opts.onFatalConnectionLoss,
    onConnectionStatus: opts.onConnectionStatus,
  };
  return { deps, state, nc };
}

function busConfig(): AnyRecord {
  return {
    sessionId: "63",
    sessionStorageKey: "63",
    natsURL: "nats://test:4222",
    natsToken: "t",
    natsStream: "TANK_SESSION_BUS",
    operatorInternalURL: "",
    operatorTokenPath: "",
    ownerEmail: "user@example.com",
  };
}

void test("connect hardens reconnection: unlimited attempts, waitOnFirstConnect", async () => {
  const { deps, state } = fakeDeps({ iterators: [blockingIterator([])] });
  const bus = new SharedSessionBus(busConfig() as never, "claude", deps as never);
  const stop = await bus.startCommandConsumer(async () => {}, undefined as never);
  assert.equal(state.connectOptions?.maxReconnectAttempts, -1);
  assert.equal(state.connectOptions?.waitOnFirstConnect, true);
  await stop();
  await bus.close();
});

void test("connect uses per-session user and projected token file authenticator", async () => {
  const dir = await mkdtemp(join(tmpdir(), "tank-nats-auth-"));
  const tokenPath = join(dir, "token");
  await writeFile(tokenPath, "projected-token\n", { mode: 0o600 });
  const { deps, state } = fakeDeps({ iterators: [blockingIterator([])] });
  const cfg = {
    ...busConfig(),
    natsToken: "",
    natsUser: "slot-a:63",
    natsPasswordFile: tokenPath,
  };
  const bus = new SharedSessionBus(cfg as never, "claude", deps as never);
  const stop = await bus.startCommandConsumer(async () => {}, undefined as never);
  assert.equal(state.connectOptions?.token, undefined);
  assert.equal(typeof state.connectOptions?.authenticator, "function");
  const authenticator = state.connectOptions?.authenticator as () => AnyRecord;
  assert.deepEqual(authenticator(), {
    user: "slot-a:63",
    pass: "projected-token",
  });
  await stop();
  await bus.close();
});

void test("connect rejects partial per-session NATS auth config", async () => {
  const { deps } = fakeDeps({ iterators: [blockingIterator([])] });
  const cfg = {
    ...busConfig(),
    natsToken: "",
    natsUser: "slot-a:63",
    natsPasswordFile: "",
  };
  const bus = new SharedSessionBus(cfg as never, "claude", deps as never);
  await assert.rejects(
    () => bus.startCommandConsumer(async () => {}, undefined as never),
    /NATS_USER and NATS_PASSWORD_FILE must be set together/,
  );
});

void test("supervised consumer restarts after iterator death and redelivers", async () => {
  const restarts: string[] = [];
  const seen: string[] = [];
  const second = blockingIterator([fakeMessage("cmd-2")]);
  const { deps } = fakeDeps({
    iterators: [oneShotIterator([fakeMessage("cmd-1")]), second],
    onConsumerRestart: (kind) => restarts.push(kind),
  });
  const bus = new SharedSessionBus(busConfig() as never, "claude", deps as never);
  const stop = await bus.startCommandConsumer(async (record: AnyRecord) => {
    seen.push(String(record.command_id));
  }, undefined as never);

  // First iterator yields cmd-1 then dies; the supervisor backs off
  // (base 1s) and resumes on a fresh iterator that yields cmd-2.
  await waitUntil(() => seen.includes("cmd-2"), 5_000);
  assert.deepEqual(seen, ["cmd-1", "cmd-2"]);
  assert.deepEqual(restarts, ["command"]);
  await stop();
  await bus.close();
});

void test("stop ends supervision: no acquisitions after stop", async () => {
  const { deps, state } = fakeDeps({
    iterators: [blockingIterator([fakeMessage("cmd-1")])],
  });
  const bus = new SharedSessionBus(busConfig() as never, "claude", deps as never);
  const stop = await bus.startCommandConsumer(async () => {}, undefined as never);
  await waitUntil(() => state.consumerGets === 1, 2_000);
  await stop();
  const acquisitions = state.consumerGets;
  await sleep(1_500); // longer than the restart base delay
  assert.equal(state.consumerGets, acquisitions);
  await bus.close();
});

void test("permanent close fires the fatal hook; graceful close does not", async () => {
  // Permanent close: closed() resolves while closing flag is unset.
  let fatal = 0;
  const a = fakeDeps({
    iterators: [blockingIterator([])],
    onFatalConnectionLoss: () => {
      fatal += 1;
    },
  });
  const busA = new SharedSessionBus(busConfig() as never, "claude", a.deps as never);
  const stopA = await busA.startCommandConsumer(async () => {}, undefined as never);
  a.state.closedResolve?.(new Error("authorization revoked"));
  await waitUntil(() => fatal === 1, 2_000);
  assert.equal(fatal, 1);
  await stopA();

  // Graceful close: close() sets the closing flag before closed() resolves.
  let fatalB = 0;
  const b = fakeDeps({
    iterators: [blockingIterator([])],
    onFatalConnectionLoss: () => {
      fatalB += 1;
    },
  });
  const busB = new SharedSessionBus(busConfig() as never, "claude", b.deps as never);
  const stopB = await busB.startCommandConsumer(async () => {}, undefined as never);
  await stopB();
  await busB.close();
  await sleep(50);
  assert.equal(fatalB, 0);
});

void test("isHealthy reflects permanent closure", async () => {
  const { deps, state, nc } = fakeDeps({ iterators: [blockingIterator([])] });
  const bus = new SharedSessionBus(busConfig() as never, "claude", deps as never);
  assert.equal(bus.isHealthy(), true, "healthy before boot");
  const stop = await bus.startCommandConsumer(async () => {}, undefined as never);
  assert.equal(bus.isHealthy(), true, "healthy while connected");
  state.isClosed = true;
  assert.equal(bus.isHealthy(), false, "unhealthy once permanently closed");
  await stop();
  void nc; // (closing the fake would re-resolve closed)
});

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

async function waitUntil(check: () => boolean, timeoutMs: number): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if (check()) return;
    await sleep(25);
  }
  if (!check()) throw new Error("condition not reached in time");
}
