export type AgentAvatar = {
  id: string;
  name: string;
  src: string;
};

// JP1 cast: 2 dinos + 7 humans. PNG files live in
// frontend/public/assets/avatars/ and are served at /assets/avatars/<file>.
// See ATTRIBUTION.md in that directory for sourcing/licensing notes.
//
// Render contract (see index.css .session-avatar / .run-status-avatar /
// .run-msg-ai-icon): square frame, object-fit: contain, 24-42px display
// size. The sidebar surface (.session-avatar) is circle-cropped and runs
// edge-to-edge with no backdrop or padding, so the source image itself is
// the visible shape. The JP1 sources are scene stills (not transparent
// silhouettes), so the per-slug CROPS in scripts/normalize-jp1-avatars.py
// are tuned to put the dark subject in the circle and push bright sky /
// clothing out of frame — anything brighter than the sidebar bg reads as
// a filled tile at 42px instead of a floating token.
export const AGENT_AVATARS: AgentAvatar[] = [
  // Dinos
  { id: "jp1-raptor", name: "Velociraptor", src: "/assets/avatars/jp1-raptor.png" },
  { id: "jp1-brachiosaurus", name: "Brachiosaurus", src: "/assets/avatars/jp1-brachiosaurus.png" },
  // Humans
  { id: "jp1-grant", name: "Dr. Alan Grant", src: "/assets/avatars/jp1-grant.png" },
  { id: "jp1-sattler", name: "Dr. Ellie Sattler", src: "/assets/avatars/jp1-sattler.png" },
  { id: "jp1-malcolm", name: "Dr. Ian Malcolm", src: "/assets/avatars/jp1-malcolm.png" },
  { id: "jp1-hammond", name: "John Hammond", src: "/assets/avatars/jp1-hammond.png" },
  { id: "jp1-nedry", name: "Dennis Nedry", src: "/assets/avatars/jp1-nedry.png" },
  { id: "jp1-muldoon", name: "Robert Muldoon", src: "/assets/avatars/jp1-muldoon.png" },
  { id: "jp1-arnold", name: "Ray Arnold", src: "/assets/avatars/jp1-arnold.png" },
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
