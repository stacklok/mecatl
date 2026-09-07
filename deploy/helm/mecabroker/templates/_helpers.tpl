{{- define "mecabroker.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- define "mecabroker.fullname" -}}
{{- default (printf "%s-%s" .Release.Name (include "mecabroker.name" .)) .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- define "mecabroker.labels" -}}
app.kubernetes.io/name: {{ include "mecabroker.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: broker
{{- end -}}
