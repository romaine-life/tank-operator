export interface TurnActivityActiveSummary {
  turnId?: string;
  status?: string;
  active?: boolean;
}

export function turnActivityShellIsDurablyActive(
  summary: TurnActivityActiveSummary | undefined,
): boolean {
  return summary?.active === true || summary?.status === "active";
}

export function turnActivityGroupIsActive(
  summary: TurnActivityActiveSummary | undefined,
  turnId: string,
  activeTurnId: string | null,
): boolean {
  if (turnActivityShellIsDurablyActive(summary)) return true;
  const active = activeTurnId?.trim() ?? "";
  if (!active) return false;
  const shellTurnId = (summary?.turnId ?? turnId).trim();
  return shellTurnId === active;
}
