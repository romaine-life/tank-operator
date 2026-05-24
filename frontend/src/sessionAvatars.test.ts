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
  assert.equal(getAgentAvatarPool().some((avatar) => avatar.id === system.id), false);
});
