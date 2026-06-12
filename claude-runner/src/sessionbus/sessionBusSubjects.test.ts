import { test } from "node:test";
import assert from "node:assert/strict";

import {
  commandSubject,
  controlSubject,
  eventSubject,
  eventSubjectFilter,
  SharedSessionBus,
} from "../../../runner-shared/sessionBus.js";

test("runner session bus subjects include scope and session tokens", () => {
  assert.equal(eventSubject("17"), "tank.session.ZGVmYXVsdA.MTc.events");
  assert.equal(
    eventSubject("tank-operator-slot-3:17"),
    "tank.session.dGFuay1vcGVyYXRvci1zbG90LTM.MTc.events",
  );
  // Commands ride the WorkQueue command stream's parallel `tank.cmd.>`
  // namespace (issue #1076 item 2) — JetStream forbids overlapping
  // subjects across streams, so command/control subjects MUST diverge
  // from the event namespace. Lockstep twins: backend-go's
  // sessionbus.CommandStreamSubject / ControlStreamSubject.
  assert.equal(
    commandSubject("tank-operator-slot-3:17", "codex_gui"),
    "tank.cmd.dGFuay1vcGVyYXRvci1zbG90LTM.MTc.commands.codex_gui",
  );
  assert.equal(
    controlSubject("tank-operator-slot-3:17", "codex-gui"),
    "tank.cmd.dGFuay1vcGVyYXRvci1zbG90LTM.MTc.control.codex-gui",
  );
  assert.equal(
    eventSubjectFilter("tank-operator-slot-3"),
    "tank.session.dGFuay1vcGVyYXRvci1zbG90LTM.*.events",
  );

  const legacySlotEvent = "tank.session.dGFuay1vcGVyYXRvci1zbG90LTM6MTc.events";
  assert.notEqual(eventSubject("tank-operator-slot-3:17"), legacySlotEvent);
});

test("runner command consumers use scoped durable names", () => {
  const deps = {
    connect: async () => null,
    jetstream: () => null,
    jetstreamManager: async () => null,
    AckPolicy: {},
    DeliverPolicy: {},
    ReplayPolicy: {},
    nanos: (millis: number) => millis,
  };
  const bus = new SharedSessionBus(
    {
      sessionId: "17",
      sessionStorageKey: "tank-operator-slot-3:17",
      ownerEmail: "user@example.com",
      natsURL: "nats://example.invalid:4222",
      natsStream: "TANK_SESSION_BUS",
      operatorInternalURL: "",
      operatorTokenPath: "",
    },
    "codex-gui",
    deps,
  ) as unknown as {
    consumerName(): string;
    controlConsumerName(): string;
  };

  assert.equal(bus.consumerName(), "codex_gui_dGFuay1vcGVyYXRvci1zbG90LTM_MTc");
  assert.equal(bus.controlConsumerName(), "codex_gui_control_dGFuay1vcGVyYXRvci1zbG90LTM_MTc");

  const legacyStorageToken = "dGFuay1vcGVyYXRvci1zbG90LTM6MTc";
  assert.notEqual(bus.consumerName(), `codex_gui_${legacyStorageToken}`);
  assert.notEqual(bus.controlConsumerName(), `codex_gui_control_${legacyStorageToken}`);
});
