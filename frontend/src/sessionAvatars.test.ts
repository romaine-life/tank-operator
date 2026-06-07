import { test, expect } from "vitest";
import {
  AGENT_AVATARS,
  getAgentAvatarPool,
  getSessionAvatarByID,
  getSystemAvatarByID,
  setRuntimeAvatarsForTest,
  type AgentAvatar,
} from "./sessionAvatars";

test("runtime avatars extend the agent pool without removing built-ins", () => {
  setRuntimeAvatarsForTest([]);
  expect(getAgentAvatarPool().some((avatar) => avatar.id === AGENT_AVATARS[0].id)).toBe(true);

  const custom: AgentAvatar = {
    id: "custom-agent",
    kind: "agent",
    name: "Custom Agent",
    src: "blob:agent",
    custom: true,
  };
  setRuntimeAvatarsForTest([custom]);
  expect(getAgentAvatarPool().some((avatar) => avatar.id === custom.id)).toBe(true);
  expect(getAgentAvatarPool().some((avatar) => avatar.id === AGENT_AVATARS[0].id)).toBe(true);
});

test("built-in agent avatars use their icon as the backing image", () => {
  for (const avatar of AGENT_AVATARS) {
    expect(avatar.backingSrc).toBe(avatar.src);
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
  expect(matching.length).toBe(1);
  expect(matching[0].src).toBe("blob:seeded-agent");
});

test("assigned agent avatar resolves by durable id", () => {
  const custom: AgentAvatar = {
    id: "assigned-agent",
    kind: "agent",
    name: "Assigned Agent",
    src: "blob:assigned-agent",
    custom: true,
  };
  setRuntimeAvatarsForTest([custom]);

  expect(getSessionAvatarByID(custom.id)?.id).toBe(custom.id);
});

test("session avatars require a durable assigned avatar id", () => {
  setRuntimeAvatarsForTest([]);

  expect(getSessionAvatarByID()).toBe(null);
  expect(getSessionAvatarByID("unknown-avatar")).toBe(null);
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

  expect(getSystemAvatarByID()).toBe(null);
  expect(getSystemAvatarByID("custom-system")?.id).toBe("custom-system");
  expect(getAgentAvatarPool().some((avatar) => avatar.id === system.id)).toBe(false);
});
