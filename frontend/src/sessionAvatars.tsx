import type { KeyboardEvent as ReactKeyboardEvent, MouseEvent as ReactMouseEvent } from "react";
import { authedFetch } from "./auth";
import { openAvatarPreview } from "./avatarPreview";

export type AvatarKind = "agent" | "system";

export type AgentAvatar = {
  id: string;
  name: string;
  kind: AvatarKind;
  src: string;
  backingSrc?: string;
  custom?: boolean;
};

type AvatarCatalogEntry = {
  id: string;
  kind: AvatarKind;
  name: string;
  avatar_url: string;
  backing_url: string;
};

// JP1 cast: 1 dino + 7 humans (brachiosaurus was dropped — the
// JP-Brachiosaur scene still didn't survive the 42px circle clip; the
// dino body alone wasn't recognizable as a brachio, and the wider crop
// that kept the iconic neck visible read as a bright sky tile instead of
// an avatar token). PNG files live in
// frontend/public/assets/avatars/ and are served at /assets/avatars/<file>.
// See ATTRIBUTION.md in that directory for sourcing/licensing notes.
//
// Render contract (see index.css .session-avatar / .run-status-avatar /
// .run-msg-ai-icon): square frame, object-fit: contain, 22-42px display
// size. All three surfaces are circle-cropped and run edge-to-edge with
// no backdrop or padding, so the source image itself is the visible
// shape. The JP1 sources are scene stills (not transparent silhouettes),
// so the per-slug CROPS in scripts/normalize-jp1-avatars.py are tuned to
// put the dark subject in the circle and push bright sky / clothing out
// of frame — anything brighter than the sidebar bg reads as a filled
// tile at 42px instead of a floating token.
export const AGENT_AVATARS: AgentAvatar[] = [
  // Dinos
  { id: "jp1-raptor", kind: "agent", name: "Velociraptor", src: "/assets/avatars/jp1-raptor.png" },
  // Humans
  { id: "jp1-grant", kind: "agent", name: "Dr. Alan Grant", src: "/assets/avatars/jp1-grant.png" },
  { id: "jp1-sattler", kind: "agent", name: "Dr. Ellie Sattler", src: "/assets/avatars/jp1-sattler.png" },
  { id: "jp1-malcolm", kind: "agent", name: "Dr. Ian Malcolm", src: "/assets/avatars/jp1-malcolm.png" },
  { id: "jp1-hammond", kind: "agent", name: "John Hammond", src: "/assets/avatars/jp1-hammond.png" },
  { id: "jp1-nedry", kind: "agent", name: "Dennis Nedry", src: "/assets/avatars/jp1-nedry.png" },
  { id: "jp1-muldoon", kind: "agent", name: "Robert Muldoon", src: "/assets/avatars/jp1-muldoon.png" },
  { id: "jp1-arnold", kind: "agent", name: "Ray Arnold", src: "/assets/avatars/jp1-arnold.png" },
];

let runtimeAgentAvatars: AgentAvatar[] = [];
let runtimeSystemAvatars: AgentAvatar[] = [];
let runtimeObjectURLs: string[] = [];

function revokeRuntimeObjectURLs() {
  if (typeof URL === "undefined") return;
  for (const objectURL of runtimeObjectURLs) {
    URL.revokeObjectURL(objectURL);
  }
  runtimeObjectURLs = [];
}

function isCatalogEntry(value: unknown): value is AvatarCatalogEntry {
  if (!value || typeof value !== "object") return false;
  const entry = value as Record<string, unknown>;
  return (
    typeof entry.id === "string" &&
    (entry.kind === "agent" || entry.kind === "system") &&
    typeof entry.name === "string" &&
    typeof entry.avatar_url === "string" &&
    typeof entry.backing_url === "string"
  );
}

async function loadAvatarImage(entry: AvatarCatalogEntry): Promise<AgentAvatar | null> {
  try {
    const res = await authedFetch(entry.avatar_url);
    if (!res.ok) return null;
    const src = URL.createObjectURL(await res.blob());
    runtimeObjectURLs.push(src);
    return {
      id: entry.id,
      kind: entry.kind,
      name: entry.name,
      src,
      backingSrc: entry.backing_url,
      custom: true,
    };
  } catch {
    return null;
  }
}

export async function loadRuntimeAvatarCatalog(): Promise<number> {
  if (typeof URL === "undefined") return 0;
  const res = await authedFetch("/api/avatars");
  if (!res.ok) throw new Error(`avatar catalog fetch failed: ${res.status}`);
  const body = (await res.json()) as { entries?: unknown };
  const entries = Array.isArray(body.entries)
    ? body.entries.filter(isCatalogEntry)
    : [];
  revokeRuntimeObjectURLs();
  const avatars = (await Promise.all(entries.map(loadAvatarImage))).filter(
    (avatar): avatar is AgentAvatar => avatar !== null,
  );
  runtimeAgentAvatars = avatars.filter((avatar) => avatar.kind === "agent");
  runtimeSystemAvatars = avatars.filter((avatar) => avatar.kind === "system");
  return avatars.length;
}

export function setRuntimeAvatarsForTest(avatars: AgentAvatar[]) {
  revokeRuntimeObjectURLs();
  runtimeAgentAvatars = avatars.filter((avatar) => avatar.kind === "agent");
  runtimeSystemAvatars = avatars.filter((avatar) => avatar.kind === "system");
}

export function getAgentAvatarPool(): AgentAvatar[] {
  return [...AGENT_AVATARS, ...runtimeAgentAvatars];
}

function hashString(value: string): number {
  let hash = 2166136261;
  for (let i = 0; i < value.length; i++) {
    hash ^= value.charCodeAt(i);
    hash = Math.imul(hash, 16777619);
  }
  return hash >>> 0;
}

function chooseAvatar(pool: AgentAvatar[], seed: string): AgentAvatar | null {
  let best: AgentAvatar | null = null;
  let bestScore = -1;
  for (const avatar of pool) {
    const score = hashString(`${seed}\x1f${avatar.id}`);
    if (score > bestScore) {
      best = avatar;
      bestScore = score;
    }
  }
  return best;
}

export function getSessionAvatar(sessionId: string): AgentAvatar {
  if (runtimeAgentAvatars.length === 0) {
    return AGENT_AVATARS[hashString(sessionId) % AGENT_AVATARS.length];
  }
  return chooseAvatar(getAgentAvatarPool(), sessionId) ?? AGENT_AVATARS[0];
}

export function getSystemAvatar(seed: string): AgentAvatar | null {
  return chooseAvatar(runtimeSystemAvatars, seed);
}

export function AgentAvatarIcon({
  avatar,
  className,
}: {
  avatar: AgentAvatar;
  className?: string;
}) {
  const openPreview = (
    event:
      | ReactMouseEvent<HTMLImageElement>
      | ReactKeyboardEvent<HTMLImageElement>,
  ) => {
    openAvatarPreview(
      {
        name: avatar.name,
        avatarSrc: avatar.src,
        backingSrc: avatar.backingSrc,
        kind: avatar.kind,
      },
      event,
    );
  };
  return (
    <img
      className={className}
      src={avatar.src}
      alt=""
      title={avatar.name}
      draggable={false}
      role="button"
      tabIndex={0}
      aria-label={`Preview ${avatar.name}`}
      onClick={openPreview}
      onKeyDown={(event) => {
        if (event.key === "Enter" || event.key === " ") openPreview(event);
      }}
    />
  );
}
