import { READABLE_ROOTS, isDeniedPath } from "./workspaceRoots";

// Roots whose paths are linkified in chat prose — the same browsable roots the
// files panel bookmarks. `~` expands to /home/node. Anything outside these, or
// under a secret deny-prefix, is left as plain text (the files panel is the way
// to reach those; we don't want false links to non-files or to 403s).
const READABLE_LINK_ROOTS = READABLE_ROOTS.map((r) => r.path);
const WORKSPACE_PATH_RE =
  /(?:~\/|\/home\/node\/|\/workspace\/|\/opt\/tank\/|\/tmp\/|workspace\/)[^\s<>"'`]+/g;
const URL_RE = /https?:\/\/[^\s<>"'`]+/g;
const TRAILING_LINK_PUNCTUATION_RE = /[.,;:!?]+$/;
const INTERNAL_ABSOLUTE_HREF_PREFIXES = [
  "/api/",
  "/assets/",
  "/_",
  "/manifest.webmanifest",
];

export interface WorkspacePathTarget {
  path: string;
  line: number | null;
}

export type WorkspaceTextSegment =
  | { kind: "text"; text: string }
  | { kind: "workspace_path"; text: string; href: string };

export type LinkableTextSegment =
  | { kind: "text"; text: string }
  | { kind: "workspace_path"; text: string; href: string }
  | { kind: "url"; text: string; href: string };

function splitTrailingLinkPunctuation(value: string): { href: string; trailing: string } {
  let href = value;
  let trailing = "";
  const punctuation = href.match(TRAILING_LINK_PUNCTUATION_RE)?.[0] ?? "";
  if (punctuation) {
    href = href.slice(0, -punctuation.length);
    trailing = punctuation;
  }
  while (href.endsWith(")") && (href.match(/\(/g)?.length ?? 0) < (href.match(/\)/g)?.length ?? 0)) {
    href = href.slice(0, -1);
    trailing = `)${trailing}`;
  }
  while (href.endsWith("]") && (href.match(/\[/g)?.length ?? 0) < (href.match(/\]/g)?.length ?? 0)) {
    href = href.slice(0, -1);
    trailing = `]${trailing}`;
  }
  return { href, trailing };
}

function escapeMarkdownLinkText(text: string): string {
  return text.replace(/\\/g, "\\\\").replace(/\]/g, "\\]");
}

function markdownLinkDestination(href: string): string {
  return `<${encodeURI(href).replace(/>/g, "%3E")}>`;
}

function workspaceHrefFromTarget(target: WorkspacePathTarget): string {
  return `${target.path}${target.line === null ? "" : `:${target.line}`}`;
}

function normalizeWorkspaceFileURLDestination(rawHref: string): string | null {
  if (!/^file:/i.test(rawHref)) return null;
  const target = workspacePathFromHref(rawHref, null);
  return target ? workspaceHrefFromTarget(target) : null;
}

function isHttpUrl(href: string): boolean {
  try {
    const url = new URL(href);
    return url.protocol === "http:" || url.protocol === "https:";
  } catch {
    return false;
  }
}

function splitLineSuffix(path: string): { path: string; line: number | null } {
  const match = path.match(/:(\d+)$/);
  if (!match) return { path, line: null };
  const line = Number(match[1]);
  if (!Number.isSafeInteger(line) || line < 1) return { path, line: null };
  return { path: path.slice(0, -match[0].length), line };
}

function expandTilde(path: string): string {
  if (path === "~") return "/home/node";
  if (path.startsWith("~/")) return `/home/node/${path.slice(2)}`;
  return path;
}

function isWorkspaceHrefPath(path: string): boolean {
  const p = expandTilde(path);
  if (p === "workspace" || p.startsWith("workspace/") || p.startsWith("./")) {
    return true;
  }
  return READABLE_LINK_ROOTS.some((r) => p === r || p.startsWith(`${r}/`));
}

export function normalizeWorkspacePathTarget(rawPath: string): WorkspacePathTarget | null {
  let path = rawPath.trim();
  if (!path) return null;
  path = path.split(/[?#]/, 1)[0] ?? "";
  try {
    path = decodeURI(path);
  } catch {
    // Keep the raw path if it is not valid percent-encoded text.
  }
  path = path.replace(/\\/g, "/");
  const lineTarget = splitLineSuffix(path);
  path = expandTilde(lineTarget.path);
  // Resolve to an absolute pod path. ./x , workspace/x , and bare relative paths
  // land under /workspace; an already-absolute path keeps its root.
  if (path.startsWith("./")) path = `/workspace/${path.slice(2)}`;
  else if (path === "workspace" || path.startsWith("workspace/")) {
    path = `/${path}`;
  } else if (!path.startsWith("/")) path = `/workspace/${path}`;
  path = path.replace(/\/{2,}/g, "/").replace(/\/+$/, "");
  if (!path || path.split("/").some((seg) => seg === "..")) return null;
  // Only the browsable roots are linkified, and never a secret deny-prefix.
  if (!READABLE_LINK_ROOTS.some((r) => path.startsWith(`${r}/`))) return null;
  if (isDeniedPath(path)) return null;
  return { path, line: lineTarget.line };
}

export function normalizeWorkspacePath(rawPath: string): string | null {
  return normalizeWorkspacePathTarget(rawPath)?.path ?? null;
}

function currentBrowserOrigin(): string | null {
  return typeof window === "undefined" ? null : window.location.origin;
}

export function workspacePathFromHref(
  href: string | undefined,
  currentOrigin: string | null = currentBrowserOrigin(),
): WorkspacePathTarget | null {
  if (!href) return null;
  const trimmed = href.trim();
  if (!trimmed || trimmed.startsWith("#")) return null;

  if (trimmed.startsWith("file://")) {
    try {
      const url = new URL(trimmed);
      return isWorkspaceHrefPath(url.pathname)
        ? normalizeWorkspacePathTarget(url.pathname)
        : null;
    } catch {
      return null;
    }
  }

  if (/^https?:/i.test(trimmed)) {
    try {
      const url = new URL(trimmed);
      if (!currentOrigin || url.origin !== currentOrigin) return null;
      return isWorkspaceHrefPath(url.pathname)
        ? normalizeWorkspacePathTarget(url.pathname)
        : null;
    } catch {
      return null;
    }
  }

  if (/^[a-z][a-z0-9+.-]*:/i.test(trimmed) || trimmed.startsWith("//")) {
    return null;
  }

  if (trimmed.startsWith("/")) {
    if (INTERNAL_ABSOLUTE_HREF_PREFIXES.some((prefix) => trimmed.startsWith(prefix))) {
      return null;
    }
    if (!isWorkspaceHrefPath(trimmed)) {
      return null;
    }
    return normalizeWorkspacePathTarget(trimmed);
  }

  if (
    trimmed.startsWith("workspace/") ||
    trimmed.startsWith("./") ||
    trimmed === "~" ||
    trimmed.startsWith("~/")
  ) {
    return normalizeWorkspacePathTarget(trimmed);
  }

  return null;
}

function canLinkTextTarget(chunk: string, index: number): boolean {
  if (index === 0) return true;
  const previous = chunk[index - 1];
  if (/\s/.test(previous)) return true;
  if (previous === "(") return chunk[index - 2] !== "]";
  return previous === "[" || previous === "{";
}

interface LinkCandidate {
  start: number;
  end: number;
  href: string;
  trailing: string;
  kind: "workspace_path" | "url";
}

function collectLinkCandidates(text: string): LinkCandidate[] {
  const candidates: LinkCandidate[] = [];

  URL_RE.lastIndex = 0;
  for (const match of text.matchAll(URL_RE)) {
    const raw = match[0];
    const start = match.index ?? 0;
    if (!canLinkTextTarget(text, start)) continue;
    const { href, trailing } = splitTrailingLinkPunctuation(raw);
    if (!isHttpUrl(href)) continue;
    candidates.push({
      start,
      end: start + raw.length,
      href,
      trailing,
      kind: "url",
    });
  }

  WORKSPACE_PATH_RE.lastIndex = 0;
  for (const match of text.matchAll(WORKSPACE_PATH_RE)) {
    const raw = match[0];
    const start = match.index ?? 0;
    if (!canLinkTextTarget(text, start)) continue;
    const { href, trailing } = splitTrailingLinkPunctuation(raw);
    if (!href || href === "/workspace" || href === "workspace") continue;
    if (!normalizeWorkspacePathTarget(href)) continue;
    candidates.push({
      start,
      end: start + raw.length,
      href,
      trailing,
      kind: "workspace_path",
    });
  }

  candidates.sort((a, b) => a.start - b.start || b.end - a.end);
  const accepted: LinkCandidate[] = [];
  let coveredUntil = 0;
  for (const candidate of candidates) {
    if (candidate.start < coveredUntil) continue;
    accepted.push(candidate);
    coveredUntil = candidate.end;
  }
  return accepted;
}

function linkTargetsInTextChunk(chunk: string): string {
  const candidates = collectLinkCandidates(chunk);
  if (candidates.length === 0) return chunk;

  let out = "";
  let cursor = 0;
  for (const candidate of candidates) {
    out += chunk.slice(cursor, candidate.start);
    out += `[${escapeMarkdownLinkText(candidate.href)}](${markdownLinkDestination(candidate.href)})`;
    out += candidate.trailing;
    cursor = candidate.end;
  }
  out += chunk.slice(cursor);
  return out;
}

function findUnescaped(value: string, needle: string, start: number): number {
  for (let i = start; i < value.length; i++) {
    if (value[i] !== needle) continue;
    let slashCount = 0;
    for (let j = i - 1; j >= 0 && value[j] === "\\"; j--) slashCount++;
    if (slashCount % 2 === 0) return i;
  }
  return -1;
}

function rewriteWorkspaceFileMarkdownLinksInTextChunk(chunk: string): string {
  let out = "";
  let cursor = 0;
  let i = 0;

  while (i < chunk.length) {
    if (chunk[i] !== "[" || chunk[i - 1] === "!") {
      i++;
      continue;
    }
    const labelEnd = findUnescaped(chunk, "]", i + 1);
    if (labelEnd === -1 || chunk[labelEnd + 1] !== "(") {
      i++;
      continue;
    }

    const destStart = labelEnd + 2;
    let destValueStart = destStart;
    let destValueEnd = destStart;
    let closeParenStart = -1;

    if (chunk[destStart] === "<") {
      const angleEnd = findUnescaped(chunk, ">", destStart + 1);
      if (angleEnd === -1) {
        i++;
        continue;
      }
      destValueStart = destStart + 1;
      destValueEnd = angleEnd;
      closeParenStart = findUnescaped(chunk, ")", angleEnd + 1);
    } else {
      while (
        destValueEnd < chunk.length &&
        !/\s|\)/.test(chunk[destValueEnd])
      ) {
        destValueEnd++;
      }
      closeParenStart = findUnescaped(chunk, ")", destValueEnd);
    }

    if (closeParenStart === -1) {
      i++;
      continue;
    }

    const replacementHref = normalizeWorkspaceFileURLDestination(
      chunk.slice(destValueStart, destValueEnd),
    );
    if (!replacementHref) {
      i = closeParenStart + 1;
      continue;
    }

    out += chunk.slice(cursor, destStart);
    out += markdownLinkDestination(replacementHref);
    out += chunk.slice(
      chunk[destStart] === "<" ? destValueEnd + 1 : destValueEnd,
      closeParenStart + 1,
    );
    cursor = closeParenStart + 1;
    i = cursor;
  }

  out += chunk.slice(cursor);
  return out;
}

function linkTargetsOutsideInlineCode(line: string): string {
  let out = "";
  let chunkStart = 0;
  let i = 0;

  while (i < line.length) {
    if (line[i] !== "`") {
      i++;
      continue;
    }

    let tickEnd = i + 1;
    while (tickEnd < line.length && line[tickEnd] === "`") tickEnd++;
    const tickRun = line.slice(i, tickEnd);
    const closing = line.indexOf(tickRun, tickEnd);
    if (closing === -1) {
      i = tickEnd;
      continue;
    }

    const chunk = rewriteWorkspaceFileMarkdownLinksInTextChunk(
      line.slice(chunkStart, i),
    );
    out += linkTargetsInTextChunk(chunk);
    out += line.slice(i, closing + tickRun.length);
    i = closing + tickRun.length;
    chunkStart = i;
  }

  const chunk = rewriteWorkspaceFileMarkdownLinksInTextChunk(
    line.slice(chunkStart),
  );
  out += linkTargetsInTextChunk(chunk);
  return out;
}

export function splitLinksInText(text: string): LinkableTextSegment[] {
  const segments: LinkableTextSegment[] = [];
  let cursor = 0;

  const pushText = (value: string) => {
    if (!value) return;
    const previous = segments[segments.length - 1];
    if (previous?.kind === "text") {
      previous.text += value;
      return;
    }
    segments.push({ kind: "text", text: value });
  };

  for (const candidate of collectLinkCandidates(text)) {
    if (candidate.start > cursor) {
      pushText(text.slice(cursor, candidate.start));
    }
    segments.push({ kind: candidate.kind, text: candidate.href, href: candidate.href });
    if (candidate.trailing) {
      pushText(candidate.trailing);
    }
    cursor = candidate.end;
  }

  if (cursor < text.length) {
    pushText(text.slice(cursor));
  }
  return segments.length > 0 ? segments : [{ kind: "text", text }];
}

export function splitWorkspacePathsInText(text: string): WorkspaceTextSegment[] {
  return splitLinksInText(text).map((segment): WorkspaceTextSegment => {
    if (segment.kind === "url") return { kind: "text", text: segment.text };
    return segment;
  });
}

export function linkTextTargetsInMarkdown(markdown: string): string {
  const lines = markdown.split(/(\n)/);
  let fence: { marker: "`" | "~"; length: number } | null = null;

  return lines.map((line) => {
    if (line === "\n") return line;

    const fenceMatch = line.match(/^ {0,3}(`{3,}|~{3,})/);
    if (fence) {
      if (
        fenceMatch &&
        fenceMatch[1][0] === fence.marker &&
        fenceMatch[1].length >= fence.length
      ) {
        fence = null;
      }
      return line;
    }

    if (fenceMatch) {
      fence = {
        marker: fenceMatch[1][0] as "`" | "~",
        length: fenceMatch[1].length,
      };
      return line;
    }

    return linkTargetsOutsideInlineCode(line);
  }).join("");
}

export function linkWorkspacePathsInMarkdown(markdown: string): string {
  return linkTextTargetsInMarkdown(markdown);
}
