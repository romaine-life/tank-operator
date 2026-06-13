// Nav bookmarks and absolute-path helpers for the session files viewer.
//
// These are UI conveniences only — NOT a security boundary. The viewer is
// default-allow: the pod owner can browse anything the bypass-permissions agent
// wrote, wherever it wrote it. The backend (sessionmodel.SecretMountDenyPrefixes
// + the in-pod realpath check) is the sole authority on what is readable; the
// DENY_PREFIXES mirror here only avoids minting obviously-doomed links/routes.

export interface ReadableRoot {
  path: string;
  label: string;
  title: string;
}

// Quick-jump landing chips shown in the files breadcrumb. Mirrors
// sessionmodel.NavBookmarks intent on the backend; the server enforces the real
// boundary regardless of this list.
export const READABLE_ROOTS: ReadableRoot[] = [
  {
    path: "/workspace",
    label: "workspace",
    title: "Cloned repos and session workspace",
  },
  {
    path: "/home/node",
    label: "~",
    title: "Agent home — ~/.claude plans, agent state, configs",
  },
  { path: "/opt/tank", label: "tooling", title: "Bundled Tank skills and docs" },
  { path: "/tmp", label: "tmp", title: "Scratch files" },
];

// Mirrors sessionmodel.SecretMountDenyPrefixes. Client-side use is advisory only
// (route/link suppression); the backend re-checks on the symlink-resolved path.
// "/run/secrets/" is the realpath form of "/var/run/secrets/" (/var/run -> /run).
export const DENY_PREFIXES = [
  "/var/run/secrets/",
  "/run/secrets/",
  "/proc/",
  "/sys/",
];

// isDeniedPath reports whether an absolute path is under a secret deny-prefix.
export function isDeniedPath(absPath: string): boolean {
  return DENY_PREFIXES.some(
    (prefix) =>
      absPath === prefix.replace(/\/$/, "") || absPath.startsWith(prefix),
  );
}

// joinDir joins a child name onto an absolute parent directory.
export function joinDir(parent: string, name: string): string {
  if (!parent || parent === "/") return `/${name}`;
  return `${parent}/${name}`;
}

// parentDir returns the parent directory of an absolute path, clamped at the
// filesystem root ("/"). Used for "up" navigation in the files panel.
export function parentDir(absPath: string): string {
  if (!absPath || absPath === "/") return "/";
  const trimmed = absPath.replace(/\/+$/, "");
  const idx = trimmed.lastIndexOf("/");
  return idx <= 0 ? "/" : trimmed.slice(0, idx);
}

// homeTildeDisplay renders /home/node[/...] as ~[/...] for compact display.
export function homeTildeDisplay(absPath: string): string {
  if (absPath === "/home/node") return "~";
  if (absPath.startsWith("/home/node/")) {
    return `~/${absPath.slice("/home/node/".length)}`;
  }
  return absPath;
}
