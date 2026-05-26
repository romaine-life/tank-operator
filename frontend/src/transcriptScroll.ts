export interface TranscriptScrollSnapshot {
  scrollHeight: number;
  clientHeight: number;
  scrollTop: number;
}

export const TRANSCRIPT_VISUAL_BOTTOM_THRESHOLD_PX = 32;

export function transcriptBottomDistance(
  scrollEl: TranscriptScrollSnapshot | null | undefined,
): number | null {
  if (!scrollEl) return null;
  const distance = scrollEl.scrollHeight - scrollEl.clientHeight - scrollEl.scrollTop;
  if (!Number.isFinite(distance)) return null;
  return Math.max(0, distance);
}

export function transcriptVisuallyAtBottom(
  scrollEl: TranscriptScrollSnapshot | null | undefined,
  fallback: boolean,
): boolean {
  const distance = transcriptBottomDistance(scrollEl);
  if (distance == null) return fallback;
  return distance <= TRANSCRIPT_VISUAL_BOTTOM_THRESHOLD_PX;
}
