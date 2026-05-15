export type AgentAvatar = {
  id: string;
  name: string;
  src: string;
};

export const AGENT_AVATARS: AgentAvatar[] = [
  {
    id: "noto-sauropod",
    name: "Sauropod",
    src: "/assets/avatars/noto-sauropod.svg",
  },
  {
    id: "noto-trex",
    name: "T-Rex",
    src: "/assets/avatars/noto-trex.svg",
  },
  {
    id: "twemoji-sauropod",
    name: "Green sauropod",
    src: "/assets/avatars/twemoji-sauropod.svg",
  },
  {
    id: "twemoji-trex",
    name: "Green T-Rex",
    src: "/assets/avatars/twemoji-trex.svg",
  },
];

function hashString(value: string): number {
  let hash = 2166136261;
  for (let i = 0; i < value.length; i++) {
    hash ^= value.charCodeAt(i);
    hash = Math.imul(hash, 16777619);
  }
  return hash >>> 0;
}

export function getSessionAvatar(sessionId: string): AgentAvatar {
  return AGENT_AVATARS[hashString(sessionId) % AGENT_AVATARS.length];
}

export function AgentAvatarIcon({
  avatar,
  className,
}: {
  avatar: AgentAvatar;
  className?: string;
}) {
  return (
    <img
      className={className}
      src={avatar.src}
      alt=""
      title={avatar.name}
      draggable={false}
      aria-hidden="true"
    />
  );
}
