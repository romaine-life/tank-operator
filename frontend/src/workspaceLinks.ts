const WORKSPACE_PATH_RE = /(?:\/workspace|workspace)\/[^\s<>"'`]+/g;
const TRAILING_PATH_PUNCTUATION_RE = /[.,;:!?]+$/;

function splitTrailingPathPunctuation(path: string): { href: string; trailing: string } {
  let href = path;
  let trailing = "";
  const punctuation = href.match(TRAILING_PATH_PUNCTUATION_RE)?.[0] ?? "";
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

function canLinkWorkspacePath(chunk: string, index: number): boolean {
  if (index === 0) return true;
  const previous = chunk[index - 1];
  return /\s/.test(previous);
}

function linkWorkspacePathsInTextChunk(chunk: string): string {
  WORKSPACE_PATH_RE.lastIndex = 0;
  return chunk.replace(WORKSPACE_PATH_RE, (rawPath, index: number) => {
    if (!canLinkWorkspacePath(chunk, index)) return rawPath;
    const { href, trailing } = splitTrailingPathPunctuation(rawPath);
    if (!href || href === "/workspace" || href === "workspace") return rawPath;
    return `[${escapeMarkdownLinkText(href)}](${markdownLinkDestination(href)})${trailing}`;
  });
}

function linkWorkspacePathsOutsideInlineCode(line: string): string {
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

    out += linkWorkspacePathsInTextChunk(line.slice(chunkStart, i));
    out += line.slice(i, closing + tickRun.length);
    i = closing + tickRun.length;
    chunkStart = i;
  }

  out += linkWorkspacePathsInTextChunk(line.slice(chunkStart));
  return out;
}

export function linkWorkspacePathsInMarkdown(markdown: string): string {
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

    return linkWorkspacePathsOutsideInlineCode(line);
  }).join("");
}
