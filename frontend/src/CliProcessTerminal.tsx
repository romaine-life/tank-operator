import { useEffect, useMemo, useState } from "react";
import { ProcessTerminal } from "@sandbox-agent/react";
import { SandboxAgent } from "sandbox-agent";
import { Loader2Icon } from "lucide-react";
import {
  AUTH_TOKEN_UPDATED_EVENT,
  authedFetch,
  getStoredToken,
} from "./auth";

export function CliProcessTerminal({
  sessionId,
  visible,
}: {
  sessionId: string;
  visible: boolean;
}) {
  const [processId, setProcessId] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [clientToken, setClientToken] = useState<string | undefined>(
    () => getStoredToken() ?? undefined,
  );
  const client = useMemo(
    () =>
      new SandboxAgent({
        baseUrl: `${location.origin}/api/sessions/${sessionId}/sandbox-agent`,
        token: clientToken,
        skipHealthCheck: true,
      }),
    [clientToken, sessionId],
  );

  useEffect(() => {
    const syncToken = () => setClientToken(getStoredToken() ?? undefined);
    syncToken();
    window.addEventListener(AUTH_TOKEN_UPDATED_EVENT, syncToken);
    return () => window.removeEventListener(AUTH_TOKEN_UPDATED_EVENT, syncToken);
  }, [sessionId]);

  useEffect(() => {
    if (!visible) return;
    let cancelled = false;
    setError(null);
    authedFetch(`/api/sessions/${sessionId}/cli-process`, { method: "POST" })
      .then(async (res) => {
        if (!res.ok) throw new Error(`CLI process create failed: ${res.status}`);
        return (await res.json()) as { process_id: string };
      })
      .then((body) => {
        if (!cancelled) {
          setClientToken(getStoredToken() ?? undefined);
          setProcessId(body.process_id);
        }
      })
      .catch((err) => {
        if (!cancelled) setError(String((err as Error).message ?? err));
      });
    return () => {
      cancelled = true;
    };
  }, [sessionId, visible]);

  if (error) {
    return <div className="run-shell-error">{error}</div>;
  }
  if (!processId) {
    return (
      <div className="run-shell-loading">
        <Loader2Icon size={18} className="run-spin" aria-hidden="true" />
        <span>starting CLI...</span>
      </div>
    );
  }
  return (
    <ProcessTerminal
      client={client}
      processId={processId}
      className="run-process-terminal"
      height="100%"
      showStatusBar={false}
    />
  );
}
