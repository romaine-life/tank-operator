import assert from "node:assert/strict";
import test from "node:test";
import {
  AGENT_AVATARS,
  getAgentAvatarPool,
  getSessionAvatar,
  getSystemAvatar,
  setRuntimeAvatarsForTest,
  type AgentAvatar,
} from "./sessionAvatars";

test("runtime avatars extend the agent pool without removing built-ins", () => {
  setRuntimeAvatarsForTest([]);
  assert.equal(getAgentAvatarPool().some((avatar) => avatar.id === AGENT_AVATARS[0].id), true);

  const custom: AgentAvatar = {
    id: "custom-agent",
    kind: "agent",
    name: "Custom Agent",
    src: "blob:agent",
    custom: true,
  };
  setRuntimeAvatarsForTest([custom]);
  assert.equal(getAgentAvatarPool().some((avatar) => avatar.id === custom.id), true);
  assert.equal(getAgentAvatarPool().some((avatar) => avatar.id === AGENT_AVATARS[0].id), true);
});

test("built-in agent avatars use their icon as the backing image", () => {
  for (const avatar of AGENT_AVATARS) {
    assert.equal(avatar.backingSrc, avatar.src);
  }
});

test("runtime avatars replace built-in avatars with the same id", () => {
  const builtIn = AGENT_AVATARS[0];
  setRuntimeAvatarsForTest([{
    id: builtIn.id,
    kind: "agent",
    name: builtIn.name,
    src: "blob:seeded-agent",
    backingSrc: "/api/avatars/seeded/backing",
  }]);
  const matching = getAgentAvatarPool().filter((avatar) => avatar.id === builtIn.id);
  assert.equal(matching.length, 1);
  assert.equal(matching[0].src, "blob:seeded-agent");
});

test("assigned agent avatar wins over hash selection", () => {
  const custom: AgentAvatar = {
    id: "assigned-agent",
    kind: "agent",
    name: "Assigned Agent",
    src: "blob:assigned-agent",
    custom: true,
  };
  setRuntimeAvatarsForTest([custom]);

  assert.equal(getSessionAvatar("session-1", custom.id)?.id, custom.id);
});

test("session avatars require a durable assigned avatar id", () => {
  setRuntimeAvatarsForTest([]);

  assert.equal(getSessionAvatar("session-1"), null);
  assert.equal(getSessionAvatar("session-1", "unknown-avatar"), null);
});

test("system avatars are separate from agent avatars", () => {
  const system: AgentAvatar = {
    id: "custom-system",
    kind: "system",
    name: "Custom System",
    src: "blob:system",
    custom: true,
  };
  setRuntimeAvatarsForTest([system]);

  assert.equal(getSystemAvatar("session-1"), null);
  assert.equal(getSystemAvatar("session-2", "custom-system")?.id, "custom-system");
  assert.equal(getAgentAvatarPool().some((avatar) => avatar.id === system.id), false);
});
