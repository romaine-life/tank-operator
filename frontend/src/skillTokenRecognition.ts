export interface SkillTokenCandidate {
  name: string;
}

export interface RecognizedSkillToken {
  leadingText: string;
  tokenText: string;
  skillName: string;
  trailingText: string;
}

const skillTokenPattern = /^(\s*)([$/])([A-Za-z0-9_-]{1,64})(?=$|\s)/;

export function recognizeLeadingSkillToken(
  text: string,
  skills: SkillTokenCandidate[],
  triggerPrefix: "$" | "/",
): RecognizedSkillToken | null {
  if (!text || skills.length === 0) return null;
  const match = text.match(skillTokenPattern);
  if (!match) return null;
  const [, leadingText, prefix, skillName] = match;
  if (prefix !== triggerPrefix) return null;
  if (!skills.some((skill) => skill.name === skillName)) return null;
  const tokenText = `${prefix}${skillName}`;
  return {
    leadingText,
    tokenText,
    skillName,
    trailingText: text.slice(leadingText.length + tokenText.length),
  };
}
