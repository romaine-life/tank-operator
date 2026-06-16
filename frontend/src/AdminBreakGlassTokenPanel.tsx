import { useState } from "react";
import { CheckIcon, CopyIcon, Loader2Icon, ShieldAlertIcon } from "lucide-react";
import { authedFetch } from "./auth";

type Props = {
  // The admin's effective session scope; forwarded so the backend resolves the
  // target session in the right registry (prod vs a test slot).
  sessionScope: string;
};

type MintResult = {
  token: string;
  expiresAt: string;
  actorEmail: string;
};

export function AdminBreakGlassTokenPanel({ sessionScope }: Props) {
  const [sessionId, setSessionId] = useState("");
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [result, setResult] = useState<MintResult | null>(null);
  const [copied, setCopied] = useState(false);

  const canSubmit = sessionId.trim() !== "" && !pending;

  async function mint() {
    if (!canSubmit) return;
    setPending(true);
    setError(null);
    setResult(null);
    setCopied(false);
    try {
      const id = encodeURIComponent(sessionId.trim());
      const scope = encodeURIComponent(sessionScope);
      const res = await authedFetch(
        `/api/admin/sessions/${id}/auth-token?session_scope=${scope}`,
        {
          method: "POST",
          headers: { "content-type": "application/json" },
          body: "{}",
        },
      );
      const text = await res.text();
      let parsed: Record<string, any> = {};
      if (text.trim()) {
        try {
          parsed = JSON.parse(text);
        } catch {
          parsed = { detail: text };
        }
      }
      if (!res.ok) {
        throw new Error(String(parsed.detail ?? `mint request returned ${res.status}`));
      }
      setResult({
        token: String(parsed.token ?? ""),
        expiresAt: String(parsed.expires_at ?? ""),
        actorEmail: String(parsed.actor_email ?? ""),
      });
    } catch (err) {
      setError(err instanceof Error ? err.message : "mint failed");
    } finally {
      setPending(false);
    }
  }

  async function copyToken() {
    if (!result?.token) return;
    await navigator.clipboard.writeText(result.token);
    setCopied(true);
    window.setTimeout(() => setCopied(false), 1800);
  }

  return (
    <div className="run-settings-diagnostics break-glass-token">
      <div className="run-settings-diagnostics-head">
        <span className="run-settings-link-label">
          <ShieldAlertIcon className="run-settings-link-icon" aria-hidden="true" />
          <span>Mint an auth token for a stuck agent</span>
        </span>
        <span className="run-settings-scope-value">{sessionScope}</span>
      </div>
      <p className="run-settings-observability-note">
        Mints the auth.romaine.life <code>role=service</code> token a session's agent would
        normally receive, on behalf of that session's owner — the credential the system expects an
        agent to authenticate with. Paste it into the stuck agent's chat; it authenticates to Tank
        and the MCP servers as that service principal. The token is short-lived (~15&nbsp;min) and
        is never stored by Tank — treat it as a live credential.
      </p>
      <label className="run-settings-label">
        Session ID
        <input
          className="break-glass-token-input"
          value={sessionId}
          onChange={(event) => setSessionId(event.target.value)}
          placeholder="987"
          inputMode="numeric"
        />
      </label>
      <div className="break-glass-token-actions">
        <button
          type="button"
          className="btn-primary break-glass-token-btn"
          disabled={!canSubmit}
          onClick={mint}
        >
          {pending ? <Loader2Icon aria-hidden="true" /> : <ShieldAlertIcon aria-hidden="true" />}
          <span>Mint auth token</span>
        </button>
      </div>
      {error && <div className="run-settings-observability-note is-critical">{error}</div>}
      {result && (
        <div className="break-glass-token-result" role="status">
          <div className="break-glass-token-meta">
            <span className="run-settings-observability-chip is-healthy">service</span>
            <span className="run-settings-observability-text">
              {result.actorEmail}
              {result.expiresAt ? <span> · expires {result.expiresAt}</span> : null}
            </span>
            <button type="button" className="run-settings-test-btn" onClick={copyToken}>
              {copied ? <CheckIcon aria-hidden="true" /> : <CopyIcon aria-hidden="true" />}
              <span>{copied ? "Copied" : "Copy token"}</span>
            </button>
          </div>
          <pre className="break-glass-token-value">{result.token}</pre>
        </div>
      )}
    </div>
  );
}
