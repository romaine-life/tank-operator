export type AgentAvatar = {
  id: string;
  name: string;
  src: string;
};

// JP1 cast: 6 dinos + 8 humans. PNG files live in
// frontend/public/assets/avatars/ and are served at /assets/avatars/<file>.
// See ATTRIBUTION.md in that directory for sourcing/licensing notes.
//
// Render contract (see index.css .session-avatar / .run-status-avatar /
// .run-msg-ai-icon): square frame, object-fit: contain, 24-42px display
// size, light translucent background with inset shadow. Source PNGs should
// be square-ish, transparent background, head-and-shoulders or silhouette
// framing so they read at 24px.
export const AGENT_AVATARS: AgentAvatar[] = [
  // Dinos
  { id: "jp1-trex", name: "Tyrannosaurus rex", src: "/assets/avatars/jp1-trex.png" },
  { id: "jp1-raptor", name: "Velociraptor", src: "/assets/avatars/jp1-raptor.png" },
  { id: "jp1-brachiosaurus", name: "Brachiosaurus", src: "/assets/avatars/jp1-brachiosaurus.png" },
  { id: "jp1-dilophosaurus", name: "Dilophosaurus", src: "/assets/avatars/jp1-dilophosaurus.png" },
  { id: "jp1-triceratops", name: "Triceratops", src: "/assets/avatars/jp1-triceratops.png" },
  { id: "jp1-gallimimus", name: "Gallimimus", src: "/assets/avatars/jp1-gallimimus.png" },
  // Humans
  { id: "jp1-grant", name: "Dr. Alan Grant", src: "/assets/avatars/jp1-grant.png" },
  { id: "jp1-sattler", name: "Dr. Ellie Sattler", src: "/assets/avatars/jp1-sattler.png" },
  { id: "jp1-malcolm", name: "Dr. Ian Malcolm", src: "/assets/avatars/jp1-malcolm.png" },
  { id: "jp1-hammond", name: "John Hammond", src: "/assets/avatars/jp1-hammond.png" },
  { id: "jp1-nedry", name: "Dennis Nedry", src: "/assets/avatars/jp1-nedry.png" },
  { id: "jp1-muldoon", name: "Robert Muldoon", src: "/assets/avatars/jp1-muldoon.png" },
  { id: "jp1-arnold", name: "Ray Arnold", src: "/assets/avatars/jp1-arnold.png" },
  { id: "jp1-gennaro", name: "Donald Gennaro", src: "/assets/avatars/jp1-gennaro.png" },
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
