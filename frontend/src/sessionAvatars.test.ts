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
  const builtIn = getSessionAvatar("session-1");
  assert.equal(AGENT_AVATARS.some((avatar) => avatar.id === builtIn.id), true);

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

test("fallback agent avatars use their icon as the backing image", () => {
  for (const avatar of AGENT_AVATARS) {
    assert.equal(avatar.backingSrc, avatar.src);
  }
});

test("runtime avatars replace fallback avatars with the same id", () => {
  const fallback = AGENT_AVATARS[0];
  setRuntimeAvatarsForTest([{
    id: fallback.id,
    kind: "agent",
    name: fallback.name,
    src: "blob:seeded-agent",
    backingSrc: "/api/avatars/seeded/backing",
  }]);
  const matching = getAgentAvatarPool().filter((avatar) => avatar.id === fallback.id);
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

  assert.equal(getSessionAvatar("session-1", custom.id).id, custom.id);
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

  assert.equal(getSystemAvatar("session-1")?.id, "custom-system");
  assert.equal(getSystemAvatar("session-2", "custom-system")?.id, "custom-system");
  assert.equal(getAgentAvatarPool().some((avatar) => avatar.id === system.id), false);
});
