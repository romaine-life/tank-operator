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
}

export interface LabeledAttachment<T extends AttachmentLabelSource> {
  file: T;
  label: string;
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
  const labels = attachments.map((attachment) => attachment.label || attachment.name);
  return composeAttachmentBlock(text, labels);
}

export function composeAttachmentPathText(text: string, paths: readonly string[]): string {
  return composeAttachmentBlock(text, paths);
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
