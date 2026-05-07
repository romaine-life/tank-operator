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
{{- else if eq .Values.namespaces.orchestrator "tank-operator" -}}
claude-code-credentials
{{- else -}}
{{ printf "%s-claude-code-credentials" .Values.namespaces.orchestrator }}
{{- end -}}
{{- end -}}
