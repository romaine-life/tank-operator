import { open, readdir, stat } from "node:fs/promises";
import path from "node:path";

import type { AgyStep } from "./adapters/antigravity.js";

interface TranscriptCursor {
  offset: number;
  pending: string;
}

interface TranscriptFile {
  file: string;
  mtime: number;
  size: number;
}

export class TranscriptTailer {
  private readonly cursors = new Map<string, TranscriptCursor>();

  constructor(private readonly agyHome: string) {}

  async snapshotExisting(): Promise<void> {
    for (const { file, size } of await this.transcriptFiles()) {
      this.cursors.set(file, { offset: size, pending: "" });
    }
  }

  async drain(onStep: (step: AgyStep) => Promise<void> | void): Promise<void> {
    for (const { file, size } of await this.transcriptFiles()) {
      const cursor = this.cursors.get(file) ?? { offset: 0, pending: "" };
      if (size < cursor.offset) {
        cursor.offset = 0;
        cursor.pending = "";
      }
      if (size <= cursor.offset) {
        this.cursors.set(file, cursor);
        continue;
      }

      const chunk = await readFileRange(file, cursor.offset, size);
      cursor.offset = size;
      await this.parseChunk(file, cursor, chunk, onStep);
      this.cursors.set(file, cursor);
    }
  }

  private async parseChunk(
    file: string,
    cursor: TranscriptCursor,
    chunk: string,
    onStep: (step: AgyStep) => Promise<void> | void,
  ): Promise<void> {
    const text = cursor.pending + chunk;
    const lines = text.split("\n");
    cursor.pending = lines.pop() ?? "";

    for (let index = 0; index < lines.length; index += 1) {
      const line = lines[index]!.trim();
      if (!line) continue;
      let step: AgyStep;
      try {
        step = JSON.parse(line) as AgyStep;
      } catch {
        cursor.pending = [line, ...lines.slice(index + 1)].join("\n");
        return;
      }
      step.conversation_id = conversationIDFromTranscriptFile(file);
      step.transcript_path = file;
      await onStep(step);
    }
  }

  private async transcriptFiles(): Promise<TranscriptFile[]> {
    const brain = path.join(this.agyHome, "brain");
    let convs: string[];
    try {
      convs = await readdir(brain);
    } catch {
      return [];
    }
    const found: TranscriptFile[] = [];
    for (const conv of convs) {
      const file = path.join(
        brain,
        conv,
        ".system_generated",
        "logs",
        "transcript_full.jsonl",
      );
      try {
        const s = await stat(file);
        found.push({ file, mtime: s.mtimeMs, size: s.size });
      } catch {
        // no transcript for this conversation yet
      }
    }
    found.sort((a, b) => a.mtime - b.mtime);
    return found;
  }
}

async function readFileRange(
  file: string,
  start: number,
  end: number,
): Promise<string> {
  const size = end - start;
  if (size <= 0) return "";
  const handle = await open(file, "r");
  try {
    const buffer = Buffer.alloc(size);
    const { bytesRead } = await handle.read(buffer, 0, size, start);
    return buffer.subarray(0, bytesRead).toString("utf8");
  } finally {
    await handle.close();
  }
}

function conversationIDFromTranscriptFile(file: string): string | undefined {
  const parts = file.split(path.sep);
  const brainIndex = parts.lastIndexOf("brain");
  if (brainIndex < 0 || brainIndex + 1 >= parts.length) return undefined;
  return parts[brainIndex + 1] || undefined;
}
