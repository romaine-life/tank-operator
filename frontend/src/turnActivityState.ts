export function isTurnActivityActive(
  turnId: string,
  activeTurnId: string | null | undefined,
): boolean {
  const normalizedTurnId = turnId.trim();
  if (!normalizedTurnId) return false;

  const normalizedActiveTurnId = activeTurnId?.trim() ?? "";
  return Boolean(normalizedActiveTurnId) && normalizedTurnId === normalizedActiveTurnId;
}
