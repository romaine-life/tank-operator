const WORKSPACE_PATH_RE = /(?:\/workspace|workspace)\/[^\s<>"'`]+/g;
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

function isWorkspaceHrefPath(path: string): boolean {
  return path === "/workspace" ||
    path.startsWith("/workspace/") ||
    path === "workspace" ||
    path.startsWith("workspace/") ||
    path.startsWith("./");
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
  path = lineTarget.path;
  if (path === "/workspace" || path === "workspace") return null;
  path = path.replace(/^\/workspace\/?/, "");
  path = path.replace(/^workspace\/+/, "");
  path = path.replace(/^\/+/, "");
  path = path.replace(/^\.\//, "");
  if (!path || path === ".") return null;
  if (path.split("/").some((seg) => seg === "..")) return null;
  return { path, line: lineTarget.line };
}

export function normalizeWorkspacePath(rawPath: string): string | null {
  return normalizeWorkspacePathTarget(rawPath)?.path ?? null;
}

export function workspacePathFromHref(href: string | undefined): WorkspacePathTarget | null {
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

  if (trimmed.startsWith("workspace/") || trimmed.startsWith("./")) {
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

    out += linkTargetsInTextChunk(line.slice(chunkStart, i));
    out += line.slice(i, closing + tickRun.length);
    i = closing + tickRun.length;
    chunkStart = i;
  }

  out += linkTargetsInTextChunk(line.slice(chunkStart));
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
