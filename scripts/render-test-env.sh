#!/usr/bin/env sh
set -eu

slot="${1:?usage: render-test-env.sh SLOT_INDEX}"
name="tank-slot-${slot}"
host="${name}.tank.dev.romaine.life"

helm template "$name" "$(dirname "$0")/../k8s" \
  --namespace "$name" \
  --post-renderer "$(dirname "$0")/test-env-post-render.sh" \
  --set "namespaces.orchestrator=${name}" \
  --set "namespaces.sessions=${name}-sessions" \
  --set "ingress.hostname=${host}" \
  --set "ingress.tlsSecret=${name}-tls" \
  --set "orchestrator.serviceAccount=${name}" \
  --set "session.serviceAccount=${name}-session" \
  --set "session.configMap=${name}-session-config" \
  --set "oauthGateway.serviceHost=claude-oauth-gateway.${name}.svc.cluster.local" \
  --set "apiProxy.serviceHost=claude-api-proxy.${name}.svc.cluster.local" \
  --set "externalSecret.githubApp.secretName=${name}-github-app-creds" \
  --set "externalSecret.codexCredentials.secretName=${name}-codex-credentials" \
  --set "externalSecret.claudeCredentials.secretName=${name}-claude-code-credentials" \
  --set "externalSecret.auth.secretName=${name}-auth" \
  --set-string "externalSecret.auth.keys[0].envVar=ENTRA_CLIENT_ID" \
  --set-string "externalSecret.auth.keys[0].kvKey=tank-operator-test-oauth-client-id" \
  --set-string "externalSecret.auth.keys[1].envVar=JWT_SECRET" \
  --set-string "externalSecret.auth.keys[1].kvKey=tank-operator-jwt-secret" \
  --set-string "externalSecret.auth.keys[2].envVar=ALLOWED_EMAILS" \
  --set-string "externalSecret.auth.keys[2].kvKey=tank-operator-oauth-allowed-emails" \
  --set "credentialRefresher.configSecret=${name}-credentials-refresher-config" \
  --set "apiProxy.configSecret=${name}-api-proxy-config"
