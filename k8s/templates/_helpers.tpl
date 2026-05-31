{{/*
Resolve the Key Vault secret that stores Claude Code OAuth credentials.

Primary production keeps the historical shared name for compatibility.
Validation slots default to a namespace-scoped name so each slot has an
independent refresh-token chain. Set externalSecret.claudeCredentials.kvKey
to override explicitly.
*/}}
{{- define "tank-operator.claudeCredentialsKvKey" -}}
{{- if .Values.externalSecret.claudeCredentials.kvKey -}}
{{- .Values.externalSecret.claudeCredentials.kvKey -}}
{{- else if eq (include "tank-operator.isTestEnv" .) "true" -}}
{{- .Values.testEnv.claudeCredentialsKvKey -}}
{{- else if eq .Values.namespaces.orchestrator "tank-operator" -}}
claude-code-credentials
{{- else -}}
{{ printf "%s-claude-code-credentials" .Values.namespaces.orchestrator }}
{{- end -}}
{{- end -}}

{{- define "tank-operator.renderMode" -}}
{{- $mode := .Values.renderMode | default "normal" -}}
{{- if not (has $mode (list "normal" "warm" "hot")) -}}
{{- fail (printf "renderMode must be one of: normal, warm, hot; got %q" $mode) -}}
{{- end -}}
{{- $mode -}}
{{- end -}}

{{- define "tank-operator.isTestEnv" -}}
{{- $mode := include "tank-operator.renderMode" . -}}
{{- if or (eq $mode "warm") (eq $mode "hot") -}}true{{- else -}}false{{- end -}}
{{- end -}}

{{- define "tank-operator.slotName" -}}
{{- if eq (include "tank-operator.isTestEnv" .) "true" -}}{{ required "testEnv.slotName is required when renderMode is warm or hot" .Values.testEnv.slotName }}{{- else -}}{{ .Release.Name }}{{- end -}}
{{- end -}}

{{- define "tank-operator.renderWarm" -}}
{{- $mode := include "tank-operator.renderMode" . -}}
{{- if or (eq $mode "normal") (eq $mode "warm") -}}true{{- else -}}false{{- end -}}
{{- end -}}

{{- define "tank-operator.renderHot" -}}
{{- $mode := include "tank-operator.renderMode" . -}}
{{- if or (eq $mode "normal") (eq $mode "hot") -}}true{{- else -}}false{{- end -}}
{{- end -}}

{{- define "tank-operator.orchestratorNamespace" -}}
{{- if eq (include "tank-operator.isTestEnv" .) "true" -}}{{ .Release.Namespace }}{{- else -}}{{ .Values.namespaces.orchestrator }}{{- end -}}
{{- end -}}

{{- define "tank-operator.sessionsNamespace" -}}
{{- if eq (include "tank-operator.isTestEnv" .) "true" -}}{{ printf "%s-sessions" (include "tank-operator.slotName" .) }}{{- else -}}{{ .Values.namespaces.sessions }}{{- end -}}
{{- end -}}

{{- define "tank-operator.ingressHostname" -}}
{{- if eq (include "tank-operator.isTestEnv" .) "true" -}}{{ printf "%s.%s" (include "tank-operator.slotName" .) .Values.testEnv.recordBase }}{{- else -}}{{ .Values.ingress.hostname }}{{- end -}}
{{- end -}}

{{- define "tank-operator.ingressTlsSecret" -}}
{{- if eq (include "tank-operator.isTestEnv" .) "true" -}}{{ printf "%s-tls" (include "tank-operator.slotName" .) }}{{- else -}}{{ .Values.ingress.tlsSecret }}{{- end -}}
{{- end -}}

{{- define "tank-operator.routeListenerSetName" -}}
{{- if eq (include "tank-operator.isTestEnv" .) "true" -}}{{ .Values.testEnv.wildcardListenerSetName }}{{- else -}}tank-operator{{- end -}}
{{- end -}}

{{- define "tank-operator.routeListenerSetNamespace" -}}
{{- if eq (include "tank-operator.isTestEnv" .) "true" -}}{{ .Values.testEnv.wildcardListenerSetNamespace }}{{- else -}}{{ include "tank-operator.orchestratorNamespace" . }}{{- end -}}
{{- end -}}

{{- define "tank-operator.orchestratorServiceAccount" -}}
{{- if eq (include "tank-operator.isTestEnv" .) "true" -}}{{ include "tank-operator.slotName" . }}{{- else -}}{{ .Values.orchestrator.serviceAccount }}{{- end -}}
{{- end -}}

{{- define "tank-operator.sessionServiceAccount" -}}
{{- if eq (include "tank-operator.isTestEnv" .) "true" -}}{{ printf "%s-session" (include "tank-operator.slotName" .) }}{{- else -}}{{ .Values.session.serviceAccount }}{{- end -}}
{{- end -}}

{{- define "tank-operator.sessionConfigMap" -}}
{{- if eq (include "tank-operator.isTestEnv" .) "true" -}}{{ printf "%s-session-config" (include "tank-operator.slotName" .) }}{{- else -}}{{ .Values.session.configMap }}{{- end -}}
{{- end -}}

{{- define "tank-operator.appConfigMap" -}}
{{- if eq (include "tank-operator.isTestEnv" .) "true" -}}{{ printf "%s-app-config" (include "tank-operator.slotName" .) }}{{- else -}}{{ .Values.appConfig.configMap }}{{- end -}}
{{- end -}}

{{- define "tank-operator.sessionRegistryScope" -}}
{{- if eq (include "tank-operator.isTestEnv" .) "true" -}}{{ include "tank-operator.slotName" . }}{{- else -}}{{ .Values.session.registryScope }}{{- end -}}
{{- end -}}

{{- define "tank-operator.internalURL" -}}
{{ printf "http://tank-operator.%s.svc.cluster.local" (include "tank-operator.orchestratorNamespace" .) }}
{{- end -}}

{{- define "tank-operator.oauthGatewayHost" -}}
{{- if eq (include "tank-operator.isTestEnv" .) "true" -}}{{ printf "claude-oauth-gateway.%s.svc.cluster.local" (include "tank-operator.slotName" .) }}{{- else -}}{{ .Values.oauthGateway.serviceHost }}{{- end -}}
{{- end -}}

{{- define "tank-operator.apiProxyHost" -}}
{{- if eq (include "tank-operator.isTestEnv" .) "true" -}}{{ printf "claude-api-proxy.%s.svc.cluster.local" (include "tank-operator.slotName" .) }}{{- else -}}{{ .Values.apiProxy.serviceHost }}{{- end -}}
{{- end -}}

{{- define "tank-operator.codexApiProxyHost" -}}
{{- if eq (include "tank-operator.isTestEnv" .) "true" -}}{{ printf "codex-api-proxy.%s.svc.cluster.local" (include "tank-operator.slotName" .) }}{{- else -}}{{ .Values.codexApiProxy.serviceHost }}{{- end -}}
{{- end -}}

{{- define "tank-operator.geminiApiProxyHost" -}}
{{- if eq (include "tank-operator.isTestEnv" .) "true" -}}{{ printf "gemini-api-proxy.%s.svc.cluster.local" (include "tank-operator.slotName" .) }}{{- else -}}{{ .Values.geminiApiProxy.serviceHost }}{{- end -}}
{{- end -}}


{{- define "tank-operator.githubAppSecret" -}}
{{- if eq (include "tank-operator.isTestEnv" .) "true" -}}{{ printf "%s-github-app-creds" (include "tank-operator.slotName" .) }}{{- else -}}{{ .Values.externalSecret.githubApp.secretName }}{{- end -}}
{{- end -}}

{{- define "tank-operator.codexCredentialsSecret" -}}
{{- if eq (include "tank-operator.isTestEnv" .) "true" -}}{{ printf "%s-codex-credentials" (include "tank-operator.slotName" .) }}{{- else -}}{{ .Values.externalSecret.codexCredentials.secretName }}{{- end -}}
{{- end -}}

{{- define "tank-operator.geminiCredentialsSecret" -}}
{{- if eq (include "tank-operator.isTestEnv" .) "true" -}}{{ printf "%s-gemini-credentials" (include "tank-operator.slotName" .) }}{{- else -}}{{ .Values.externalSecret.geminiCredentials.secretName }}{{- end -}}
{{- end -}}

{{- define "tank-operator.geminiCredentialsTestSecret" -}}
{{- if eq (include "tank-operator.isTestEnv" .) "true" -}}{{ printf "%s-gemini-credentials-test" (include "tank-operator.slotName" .) }}{{- else -}}gemini-credentials-test{{- end -}}
{{- end -}}

{{- define "tank-operator.geminiCredentialsTestKvKey" -}}
{{- if eq (include "tank-operator.isTestEnv" .) "true" -}}{{ printf "%s-gemini-credentials-test" (include "tank-operator.slotName" .) }}{{- else -}}gemini-credentials-test{{- end -}}
{{- end -}}


{{- define "tank-operator.claudeCredentialsSecret" -}}
{{- if eq (include "tank-operator.isTestEnv" .) "true" -}}{{ printf "%s-claude-code-credentials" (include "tank-operator.slotName" .) }}{{- else -}}{{ .Values.externalSecret.claudeCredentials.secretName }}{{- end -}}
{{- end -}}

{{- define "tank-operator.credentialRefresherConfigSecret" -}}
{{- if eq (include "tank-operator.isTestEnv" .) "true" -}}{{ printf "%s-credentials-refresher-config" (include "tank-operator.slotName" .) }}{{- else -}}{{ .Values.credentialRefresher.configSecret }}{{- end -}}
{{- end -}}

{{- define "tank-operator.apiProxyConfigSecret" -}}
{{- if eq (include "tank-operator.isTestEnv" .) "true" -}}{{ printf "%s-api-proxy-config" (include "tank-operator.slotName" .) }}{{- else -}}{{ .Values.apiProxy.configSecret }}{{- end -}}
{{- end -}}

{{- define "tank-operator.sessionsIngressDnsEnabled" -}}
{{- if eq (include "tank-operator.isTestEnv" .) "true" -}}true{{- else -}}{{ .Values.sessionsIngress.dnsEndpoint.enabled }}{{- end -}}
{{- end -}}

{{- define "tank-operator.sessionsIngressGatewayIP" -}}
{{- if eq (include "tank-operator.isTestEnv" .) "true" -}}{{ .Values.testEnv.sessionsGatewayIP }}{{- else -}}{{ .Values.sessionsIngress.gatewayIP }}{{- end -}}
{{- end -}}
