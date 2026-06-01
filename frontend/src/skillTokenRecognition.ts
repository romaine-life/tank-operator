export interface SkillTokenCandidate {
  name: string;
  tokenText: string;
}

export interface RecognizedSkillToken {
  leadingText: string;
  tokenText: string;
  skillName: string;
  trailingText: string;
}

const skillTokenPattern = /^(\s*)([$/][A-Za-z0-9_-]{1,64})(?=$|\s)/;

export function recognizeLeadingSkillToken(
  text: string,
  skills: SkillTokenCandidate[],
): RecognizedSkillToken | null {
  if (!text || skills.length === 0) return null;
  const match = text.match(skillTokenPattern);
  if (!match) return null;
  const [, leadingText, tokenText] = match;
  const skill = skills.find((candidate) => candidate.tokenText === tokenText);
  if (!skill) return null;
  return {
    leadingText,
    tokenText,
    skillName: skill.name,
    trailingText: text.slice(leadingText.length + tokenText.length),
  };
}
