export interface AttachmentLabelSource {
  name?: string;
  type?: string;
}

export interface ExistingAttachmentLabel {
  label?: string;
  name?: string;
}

export interface AttachmentDisplaySource {
  label?: string;
  name: string;
  path?: string;
  absPath?: string;
  size?: number;
  previewUrl?: string;
  type?: string;
  kind?: string;
}

export interface LabeledAttachment<T extends AttachmentLabelSource> {
  file: T;
  label: string;
}

export interface MessageAttachmentDisplay {
  label: string;
  name: string;
  kind: "image" | "file";
  path?: string;
  absPath?: string;
  size?: number;
  previewUrl?: string;
}

const GENERIC_IMAGE_BASENAMES = new Set([
  "clipboard",
  "clipboard image",
  "image",
  "img",
  "pasted image",
  "screen shot",
  "screenshot",
]);

const GENERIC_FILE_BASENAMES = new Set([
  "attachment",
  "file",
  "unknown",
  "untitled",
]);

export function labelAttachments<T extends AttachmentLabelSource>(
  files: readonly T[],
  existing: readonly ExistingAttachmentLabel[] = [],
): LabeledAttachment<T>[] {
  const used = new Set(
    existing
      .map((item) => (item.label || item.name || "").trim())
      .filter(Boolean),
  );

  return files.map((file) => {
    const label = nextAttachmentLabel(file, used);
    used.add(label);
    return { file, label };
  });
}

export function composeAttachmentDisplayText(
  text: string,
  attachments: readonly AttachmentDisplaySource[],
): string {
  const trimmed = text.trim();
  if (trimmed || attachments.length === 0) return trimmed;
  return attachments.length === 1 ? "Attached 1 file" : `Attached ${attachments.length} files`;
}

export function composeAttachmentPathText(text: string, paths: readonly string[]): string {
  return composeAttachmentBlock(text, paths);
}

export function messageAttachmentDisplays(
  attachments: readonly AttachmentDisplaySource[],
): MessageAttachmentDisplay[] {
  return attachments
    .map((attachment): MessageAttachmentDisplay | null => {
      const label = (attachment.label || attachment.name).trim();
      const name = attachment.name.trim();
      if (!label && !name) return null;
      const path = trimOptional(attachment.path);
      const absPath = trimOptional(attachment.absPath);
      return {
        label: label || name,
        name: name || label,
        kind: isImageDisplayAttachment(attachment) ? "image" : "file",
        ...(path ? { path } : {}),
        ...(absPath ? { absPath } : {}),
        ...(typeof attachment.size === "number" && Number.isFinite(attachment.size)
          ? { size: attachment.size }
          : {}),
        ...(attachment.previewUrl ? { previewUrl: attachment.previewUrl } : {}),
      };
    })
    .filter((attachment): attachment is MessageAttachmentDisplay => attachment !== null);
}

export function splitLegacyAttachmentDisplayText(
  text: string,
): { text: string; attachments: MessageAttachmentDisplay[] } | null {
  const normalized = text.trim();
  const match = normalized.match(/^(.*?)(?:\n{2,}|\n)?Attachments:\n((?:-\s+.+(?:\n|$))+)\s*$/s);
  if (!match) return null;
  const body = match[1]?.trim() ?? "";
  const items = (match[2] ?? "")
    .split("\n")
    .map((line) => line.trim())
    .filter((line) => line.startsWith("- "))
    .map((line) => line.slice(2).trim())
    .filter(Boolean);
  if (items.length === 0) return null;
  const attachments = items.map((item): MessageAttachmentDisplay => {
    const isWorkspacePath = item.startsWith("/workspace/");
    const path = isWorkspacePath ? item.replace(/^\/workspace\/?/, "") : "";
    const name = isWorkspacePath ? item.split("/").pop() || item : item;
    return {
      label: labelForLegacyAttachmentItem(item),
      name,
      kind: isImageLikeName(item) ? "image" : "file",
      ...(path ? { path } : {}),
      ...(isWorkspacePath ? { absPath: item } : {}),
    };
  });
  return {
    text: body || (attachments.length === 1 ? "Attached 1 file" : `Attached ${attachments.length} files`),
    attachments,
  };
}

function composeAttachmentBlock(text: string, items: readonly string[]): string {
  const trimmed = text.trim();
  if (items.length === 0) return trimmed;
  const attachmentList = items.map((item) => `- ${item}`).join("\n");
  const attachmentText = `Attachments:\n${attachmentList}`;
  return trimmed ? `${trimmed}\n\n${attachmentText}` : attachmentText;
}

function nextAttachmentLabel(file: AttachmentLabelSource, used: Set<string>): string {
  const fallbackBase = isImageAttachment(file) ? "Screenshot" : "Attachment";
  const rawName = (file.name || "").trim();
  if (isGenericAttachmentName(file)) {
    return nextNumberedLabel(fallbackBase, used);
  }
  return dedupeFilenameLabel(rawName || fallbackBase, used);
}

function isImageAttachment(file: AttachmentLabelSource): boolean {
  return (file.type || "").toLowerCase().startsWith("image/");
}

function isImageDisplayAttachment(attachment: AttachmentDisplaySource): boolean {
  if (attachment.kind === "image") return true;
  if (attachment.previewUrl) return true;
  if ((attachment.type || "").toLowerCase().startsWith("image/")) return true;
  return isImageLikeName(attachment.absPath || attachment.path || attachment.name);
}

function isGenericAttachmentName(file: AttachmentLabelSource): boolean {
  const rawName = (file.name || "").trim();
  if (!rawName) return true;
  const normalized = normalizedBasename(rawName);
  if (isImageAttachment(file)) {
    return GENERIC_IMAGE_BASENAMES.has(normalized);
  }
  return GENERIC_FILE_BASENAMES.has(normalized);
}

function normalizedBasename(name: string): string {
  const pathless = name.split(/[\\/]/).at(-1) || name;
  const stem = pathless.replace(/\.[^.]+$/, "");
  return stem.toLowerCase().replace(/[\s_-]+/g, " ").trim();
}

function labelForLegacyAttachmentItem(item: string): string {
  const pathless = item.split(/[\\/]/).at(-1) || item;
  const screenshotMatch = item.match(/(?:^|\/)screenshots\/(\d+)\.[A-Za-z0-9]+$/);
  if (screenshotMatch) return `Screenshot ${screenshotMatch[1]}`;
  return pathless;
}

function isImageLikeName(name: string): boolean {
  const ext = name.toLowerCase().split(".").pop() ?? "";
  return ["png", "jpg", "jpeg", "webp", "gif", "svg", "bmp"].includes(ext);
}

function trimOptional(value: string | undefined): string {
  return typeof value === "string" ? value.trim() : "";
}

function nextNumberedLabel(base: string, used: Set<string>): string {
  let index = 1;
  while (used.has(`${base} ${index}`)) {
    index += 1;
  }
  return `${base} ${index}`;
}

function dedupeFilenameLabel(name: string, used: Set<string>): string {
  if (!used.has(name)) return name;
  const { stem, extension } = splitFilename(name);
  let index = 2;
  let candidate = `${stem} ${index}${extension}`;
  while (used.has(candidate)) {
    index += 1;
    candidate = `${stem} ${index}${extension}`;
  }
  return candidate;
}

function splitFilename(name: string): { stem: string; extension: string } {
  const dot = name.lastIndexOf(".");
  if (dot <= 0 || dot === name.length - 1) {
    return { stem: name, extension: "" };
  }
  return { stem: name.slice(0, dot), extension: name.slice(dot) };
}
