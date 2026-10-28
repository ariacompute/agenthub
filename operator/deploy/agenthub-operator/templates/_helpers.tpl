{{/* Common chart helpers for agenthub-operator. */}}

{{- define "agenthub-operator.name" -}}
agenthub-operator
{{- end -}}

{{- define "agenthub-operator.fullname" -}}
{{- printf "%s-%s" .Release.Name (include "agenthub-operator.name" .) -}}
{{- end -}}

{{- define "agenthub-operator.labels" -}}
app.kubernetes.io/name: {{ include "agenthub-operator.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/component: operator
{{- end -}}

{{- define "agenthub-operator.selectorLabels" -}}
app.kubernetes.io/name: {{ include "agenthub-operator.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}
