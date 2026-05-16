export type AgentAvatar = {
  id: string;
  name: string;
  src: string;
};

export const AGENT_AVATARS: AgentAvatar[] = [
  {
    id: "jp-amber",
    name: "Amber with mosquito",
    src: "/assets/avatars/jp-amber.svg",
  },
  {
    id: "jp-barbasol",
    name: "Barbasol can",
    src: "/assets/avatars/jp-barbasol.svg",
  },
  {
    id: "jp-jeep",
    name: "Tour jeep",
    src: "/assets/avatars/jp-jeep.svg",
  },
  {
    id: "jp-goggles",
    name: "Night-vision goggles",
    src: "/assets/avatars/jp-goggles.svg",
  },
  {
    id: "jp-gate",
    name: "Park gate",
    src: "/assets/avatars/jp-gate.svg",
  },
  {
    id: "jp-dna",
    name: "DNA double helix",
    src: "/assets/avatars/jp-dna.svg",
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
