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

// Built-in JP1 cast: 1 dino + 7 humans (brachiosaurus was dropped — the
// JP-Brachiosaur scene still didn't survive the 42px circle clip; the
// dino body alone wasn't recognizable as a brachio, and the wider crop
// that kept the iconic neck visible read as a bright sky tile instead of
// an avatar token). PNG files live in
// frontend/public/assets/avatars/ and are served at /assets/avatars/<file>.
// See ATTRIBUTION.md in that directory for sourcing/licensing notes.
//
// Render contract (see index.css .session-avatar / .run-msg-ai-icon):
// square frame, object-fit: contain, 42px display size. Both surfaces are
// circle-cropped and run edge-to-edge with no backdrop or padding, so the
// source image itself is the visible shape. The JP1 sources are scene stills (not transparent silhouettes),
// so the per-slug CROPS in scripts/normalize-jp1-avatars.py are tuned to
// put the dark subject in the circle and push bright sky / clothing out
// of frame — anything brighter than the sidebar bg reads as a filled
// tile at 42px instead of a floating token.
function builtInAgentAvatar(id: string, name: string, src: string): AgentAvatar {
  return { id, kind: "agent", name, src, backingSrc: src };
}

export const AGENT_AVATARS: AgentAvatar[] = [
  // Dinos
  builtInAgentAvatar("jp1-raptor", "Velociraptor", "/assets/avatars/jp1-raptor.png"),
  // Humans
  builtInAgentAvatar("jp1-grant", "Dr. Alan Grant", "/assets/avatars/jp1-grant.png"),
  builtInAgentAvatar("jp1-sattler", "Dr. Ellie Sattler", "/assets/avatars/jp1-sattler.png"),
  builtInAgentAvatar("jp1-malcolm", "Dr. Ian Malcolm", "/assets/avatars/jp1-malcolm.png"),
  builtInAgentAvatar("jp1-hammond", "John Hammond", "/assets/avatars/jp1-hammond.png"),
  builtInAgentAvatar("jp1-nedry", "Dennis Nedry", "/assets/avatars/jp1-nedry.png"),
  builtInAgentAvatar("jp1-muldoon", "Robert Muldoon", "/assets/avatars/jp1-muldoon.png"),
  builtInAgentAvatar("jp1-arnold", "Ray Arnold", "/assets/avatars/jp1-arnold.png"),
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

type AvatarCatalogFetch = (input: RequestInfo, init?: RequestInit) => Promise<Response>;

async function loadAvatarImage(
  entry: AvatarCatalogEntry,
  fetcher: AvatarCatalogFetch,
): Promise<AgentAvatar | null> {
  try {
    const res = await fetcher(entry.avatar_url);
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

async function loadRuntimeAvatarCatalogFrom(
  catalogURL: string,
  fetcher: AvatarCatalogFetch,
): Promise<number> {
  if (typeof URL === "undefined") return 0;
  const res = await fetcher(catalogURL);
  if (!res.ok) throw new Error(`avatar catalog fetch failed: ${res.status}`);
  const body = (await res.json()) as { entries?: unknown };
  const entries = Array.isArray(body.entries)
    ? body.entries.filter(isCatalogEntry)
    : [];
  revokeRuntimeObjectURLs();
  const avatars = (await Promise.all(entries.map((entry) => loadAvatarImage(entry, fetcher)))).filter(
    (avatar): avatar is AgentAvatar => avatar !== null,
  );
  runtimeAgentAvatars = avatars.filter((avatar) => avatar.kind === "agent");
  runtimeSystemAvatars = avatars.filter((avatar) => avatar.kind === "system");
  return avatars.length;
}

export async function loadRuntimeAvatarCatalog(): Promise<number> {
  return loadRuntimeAvatarCatalogFrom("/api/avatars", authedFetch);
}

export async function loadPublicRuntimeAvatarCatalog(shareToken: string): Promise<number> {
  return loadRuntimeAvatarCatalogFrom(
    `/api/public/message-links/${encodeURIComponent(shareToken)}/avatars`,
    fetch,
  );
}

export function setRuntimeAvatarsForTest(avatars: AgentAvatar[]) {
  revokeRuntimeObjectURLs();
  runtimeAgentAvatars = avatars.filter((avatar) => avatar.kind === "agent");
  runtimeSystemAvatars = avatars.filter((avatar) => avatar.kind === "system");
}

export function getAgentAvatarPool(): AgentAvatar[] {
  const runtimeIDs = new Set(runtimeAgentAvatars.map((avatar) => avatar.id));
  return [
    ...AGENT_AVATARS.filter((avatar) => !runtimeIDs.has(avatar.id)),
    ...runtimeAgentAvatars,
  ];
}

function findAvatarByID(pool: AgentAvatar[], avatarId?: string | null): AgentAvatar | null {
  if (!avatarId) return null;
  return pool.find((avatar) => avatar.id === avatarId) ?? null;
}

export function getSessionAvatarByID(assignedAvatarId?: string | null): AgentAvatar | null {
  return findAvatarByID(getAgentAvatarPool(), assignedAvatarId);
}

export function requireSessionAvatar(assignedAvatarId: string): AgentAvatar {
  const avatar = getSessionAvatarByID(assignedAvatarId);
  if (!avatar) {
    throw new Error(`assigned session avatar is unavailable: ${assignedAvatarId}`);
  }
  return avatar;
}

export function getSystemAvatarByID(assignedAvatarId?: string | null): AgentAvatar | null {
  return findAvatarByID(runtimeSystemAvatars, assignedAvatarId);
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
