// Parent→child session lineage for the session-bar "spawned sessions"
// chip. When an agent spawns a session (spawn_run_session /
// spawn_test_slot_session), the backend appends a ref to the calling
// (origin) session's durable `spawned_sessions` row column; the SPA reads
// it back here so the chip can list the children with working links. The
// shape mirrors backend-go sessionmodel.SpawnedSessionRef.

export type SpawnedSessionRef = {
  id: string;
  name: string;
  mode?: string;
  model?: string;
  repos?: string[];
  // url is absolute and stamped server-side by whichever operator handled
  // the spawn, so a cross-scope test-slot child carries its own slot host.
  url: string;
  created_at?: string;
};

// normalizeSpawnedSessions coerces an unknown wire value (the jsonb array,
// or a degraded/older snapshot) into a clean SpawnedSessionRef[]. Entries
// missing the load-bearing id or url are dropped so the chip never renders
// a dead link; a non-array input returns [] ("spawned nothing"). Defensive
// in the same spirit as the repos[] normalization in normalizeSession.
export function normalizeSpawnedSessions(raw: unknown): SpawnedSessionRef[] {
  if (!Array.isArray(raw)) return [];
  const out: SpawnedSessionRef[] = [];
  for (const entry of raw) {
    if (!entry || typeof entry !== "object") continue;
    const rec = entry as Record<string, unknown>;
    const id = typeof rec.id === "string" ? rec.id : "";
    const url = typeof rec.url === "string" ? rec.url : "";
    if (!id || !url) continue;
    const repos = Array.isArray(rec.repos)
      ? rec.repos.filter((r): r is string => typeof r === "string")
      : undefined;
    out.push({
      id,
      url,
      name: typeof rec.name === "string" && rec.name ? rec.name : id,
      mode: typeof rec.mode === "string" ? rec.mode : undefined,
      model: typeof rec.model === "string" ? rec.model : undefined,
      repos: repos && repos.length > 0 ? repos : undefined,
      created_at:
        typeof rec.created_at === "string" ? rec.created_at : undefined,
    });
  }
  return out;
}
