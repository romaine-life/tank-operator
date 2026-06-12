// Reconnect-delay policy for the SPA's EventSource/SSE streams.
//
// Every stream reopen is expensive on the backend: a stream-ticket mint
// (Postgres INSERT) + auth + a possible transcript backfill attempt
// through a bounded-concurrency gate. A flat 1s retry across N open
// tabs turns a backend disruption into a synchronized thundering herd
// exactly when capacity is scarcest. This helper centralizes the fix:
// exponential doubling from `baseMs` up to the `maxMs` cap, with
// ±`jitterRatio` random jitter so reconnect attempts from concurrent
// tabs decorrelate instead of arriving in lockstep waves.
//
// Usage shape (one instance per stream):
//   const backoff = createStreamBackoff();
//   // on stream-error/onerror: setTimeout(reopen, backoff.nextDelay())
//   // on successful open ("ready" event): backoff.reset()
//
// Jitter applies after the cap, so the effective delay range of a
// fully backed-off stream is [maxMs * (1 - jitterRatio),
// maxMs * (1 + jitterRatio)].

export interface StreamBackoff {
  /** Delay in ms for the next reconnect attempt; doubles per call. */
  nextDelay(): number;
  /** Return to baseMs. Call when the stream opens successfully. */
  reset(): void;
}

export interface StreamBackoffOptions {
  /** First-retry delay in ms. Default 1000. */
  baseMs?: number;
  /** Upper cap (pre-jitter) on the doubled delay in ms. Default 30000. */
  maxMs?: number;
  /** Jitter as a ± fraction of the capped delay, in [0, 1]. Default 0.2. */
  jitterRatio?: number;
  /**
   * Uniform [0, 1) source, injectable so tests can pin the jitter.
   * Default Math.random.
   */
  random?: () => number;
}

export function createStreamBackoff(
  options: StreamBackoffOptions = {},
): StreamBackoff {
  const baseMs = Math.max(0, options.baseMs ?? 1000);
  const maxMs = Math.max(baseMs, options.maxMs ?? 30000);
  const jitterRatio = Math.min(1, Math.max(0, options.jitterRatio ?? 0.2));
  const random = options.random ?? Math.random;

  let attempt = 0;

  return {
    nextDelay(): number {
      const uncapped = baseMs * 2 ** attempt;
      const capped = Math.min(uncapped, maxMs);
      // Stop growing `attempt` once the cap is reached so a long outage
      // can't push the exponent toward overflow.
      if (uncapped < maxMs) {
        attempt += 1;
      }
      // Uniform multiplier in [1 - jitterRatio, 1 + jitterRatio).
      const jitter = 1 + jitterRatio * (2 * random() - 1);
      return Math.max(0, Math.round(capped * jitter));
    },
    reset(): void {
      attempt = 0;
    },
  };
}
