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
{{- else if .Values.testEnv.enabled -}}
{{- .Values.testEnv.claudeCredentialsKvKey -}}
{{- else if eq .Values.namespaces.orchestrator "tank-operator" -}}
claude-code-credentials
{{- else -}}
{{ printf "%s-claude-code-credentials" .Values.namespaces.orchestrator }}
{{- end -}}
{{- end -}}

{{- define "tank-operator.orchestratorNamespace" -}}
{{- if .Values.testEnv.enabled -}}{{ .Release.Namespace }}{{- else -}}{{ .Values.namespaces.orchestrator }}{{- end -}}
{{- end -}}

{{- define "tank-operator.sessionsNamespace" -}}
{{- if .Values.testEnv.enabled -}}{{ printf "%s-sessions" .Release.Name }}{{- else -}}{{ .Values.namespaces.sessions }}{{- end -}}
{{- end -}}

{{- define "tank-operator.ingressHostname" -}}
{{- if .Values.testEnv.enabled -}}{{ printf "%s.%s" .Release.Name .Values.testEnv.recordBase }}{{- else -}}{{ .Values.ingress.hostname }}{{- end -}}
{{- end -}}

{{- define "tank-operator.ingressTlsSecret" -}}
{{- if .Values.testEnv.enabled -}}{{ printf "%s-tls" .Release.Name }}{{- else -}}{{ .Values.ingress.tlsSecret }}{{- end -}}
{{- end -}}

{{- define "tank-operator.orchestratorServiceAccount" -}}
{{- if .Values.testEnv.enabled -}}{{ .Release.Name }}{{- else -}}{{ .Values.orchestrator.serviceAccount }}{{- end -}}
{{- end -}}

{{- define "tank-operator.sessionServiceAccount" -}}
{{- if .Values.testEnv.enabled -}}{{ printf "%s-session" .Release.Name }}{{- else -}}{{ .Values.session.serviceAccount }}{{- end -}}
{{- end -}}

{{- define "tank-operator.sessionConfigMap" -}}
{{- if .Values.testEnv.enabled -}}{{ printf "%s-session-config" .Release.Name }}{{- else -}}{{ .Values.session.configMap }}{{- end -}}
{{- end -}}

{{- define "tank-operator.sessionRegistryScope" -}}
{{- if .Values.testEnv.enabled -}}{{ .Release.Name }}{{- else -}}{{ .Values.session.registryScope }}{{- end -}}
{{- end -}}

{{- define "tank-operator.oauthGatewayHost" -}}
{{- if .Values.testEnv.enabled -}}{{ printf "claude-oauth-gateway.%s.svc.cluster.local" .Release.Name }}{{- else -}}{{ .Values.oauthGateway.serviceHost }}{{- end -}}
{{- end -}}

{{- define "tank-operator.apiProxyHost" -}}
{{- if .Values.testEnv.enabled -}}{{ printf "claude-api-proxy.%s.svc.cluster.local" .Release.Name }}{{- else -}}{{ .Values.apiProxy.serviceHost }}{{- end -}}
{{- end -}}


{{- define "tank-operator.githubAppSecret" -}}
{{- if .Values.testEnv.enabled -}}{{ printf "%s-github-app-creds" .Release.Name }}{{- else -}}{{ .Values.externalSecret.githubApp.secretName }}{{- end -}}
{{- end -}}

{{- define "tank-operator.codexCredentialsSecret" -}}
{{- if .Values.testEnv.enabled -}}{{ printf "%s-codex-credentials" .Release.Name }}{{- else -}}{{ .Values.externalSecret.codexCredentials.secretName }}{{- end -}}
{{- end -}}

{{- define "tank-operator.claudeCredentialsSecret" -}}
{{- if .Values.testEnv.enabled -}}{{ printf "%s-claude-code-credentials" .Release.Name }}{{- else -}}{{ .Values.externalSecret.claudeCredentials.secretName }}{{- end -}}
{{- end -}}

{{- define "tank-operator.authSecret" -}}
{{- if .Values.testEnv.enabled -}}{{ printf "%s-auth" .Release.Name }}{{- else -}}{{ .Values.externalSecret.auth.secretName }}{{- end -}}
{{- end -}}

{{- define "tank-operator.credentialRefresherConfigSecret" -}}
{{- if .Values.testEnv.enabled -}}{{ printf "%s-credentials-refresher-config" .Release.Name }}{{- else -}}{{ .Values.credentialRefresher.configSecret }}{{- end -}}
{{- end -}}

{{- define "tank-operator.sessionAzureConfigSecret" -}}
{{- if .Values.testEnv.enabled -}}{{ printf "%s-session-azure-config" .Release.Name }}{{- else -}}{{ .Values.externalSecret.sessionAzureConfig.secretName }}{{- end -}}
{{- end -}}

{{- define "tank-operator.apiProxyConfigSecret" -}}
{{- if .Values.testEnv.enabled -}}{{ printf "%s-api-proxy-config" .Release.Name }}{{- else -}}{{ .Values.apiProxy.configSecret }}{{- end -}}
{{- end -}}

{{- define "tank-operator.sessionsIngressDnsEnabled" -}}
{{- if .Values.testEnv.enabled -}}true{{- else -}}{{ .Values.sessionsIngress.dnsEndpoint.enabled }}{{- end -}}
{{- end -}}

{{- define "tank-operator.sessionsIngressGatewayIP" -}}
{{- if .Values.testEnv.enabled -}}{{ .Values.testEnv.sessionsGatewayIP }}{{- else -}}{{ .Values.sessionsIngress.gatewayIP }}{{- end -}}
{{- end -}}
