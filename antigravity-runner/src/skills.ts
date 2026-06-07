import { readFile } from "node:fs/promises";
import path from "node:path";

export interface SkillExpansion {
  prompt: string;
  loaded: boolean;
  reason: "no_skill" | "loaded" | "missing";
}

const SKILL_NAME_RE = /^[A-Za-z0-9_-]{1,64}$/;
const MAX_SKILL_BYTES = 48 * 1024;

export async function expandSkillPrompt(
  prompt: string,
  skillName: unknown,
  skillsRoot = defaultSkillsRoot(),
): Promise<SkillExpansion> {
  const name = String(skillName ?? "").trim();
  if (!name) return { prompt, loaded: false, reason: "no_skill" };
  if (!SKILL_NAME_RE.test(name)) return { prompt, loaded: false, reason: "missing" };

  const skillPath = path.join(skillsRoot, name, "SKILL.md");
  let body: string;
  try {
    body = await readFile(skillPath, "utf8");
  } catch {
    return { prompt, loaded: false, reason: "missing" };
  }

  const trimmedBody = body.trim().slice(0, MAX_SKILL_BYTES);
  if (!trimmedBody) return { prompt, loaded: false, reason: "missing" };

  const userPrompt = stripSkillTrigger(name, prompt).trim();
  const expanded = [
    `Use the Tank skill "${name}" for this turn.`,
    `Skill file: ${skillPath}`,
    "",
    "Skill instructions:",
    trimmedBody,
    "",
    "User request:",
    userPrompt || prompt.trim(),
  ].join("\n");

  return { prompt: expanded, loaded: true, reason: "loaded" };
}

export function stripSkillTrigger(skillName: string, prompt: string): string {
  const escaped = skillName.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  const triggerPattern = new RegExp(`^[$/]${escaped}(?:\\s+|\\n+)?`, "i");
  return prompt.trim().replace(triggerPattern, "").trim();
}

function defaultSkillsRoot(): string {
  const explicit = process.env.ANTIGRAVITY_SKILLS_DIR?.trim();
  if (explicit) return explicit;
  const home = process.env.HOME?.trim() || "/home/node";
  return path.join(home, ".gemini", "skills");
}
