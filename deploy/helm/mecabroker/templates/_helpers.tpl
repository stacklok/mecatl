{{- define "mecabroker.validateImage" -}}
{{- if not (regexMatch "^sha256:[0-9a-f]{64}$" .Values.image.digest) -}}{{ fail "image.digest must be a lowercase sha256 digest" }}{{- end -}}
{{- if .Values.image.tag -}}{{ fail "image.tag is not permitted; use image.digest" }}{{- end -}}
{{- end -}}

{{- define "mecabroker.validateShutdownBudget" -}}
{{- $required := add (add (int .Values.drain.propagationDelaySeconds) (int .Values.drain.timeoutSeconds)) 5 -}}
{{- if le (int .Values.terminationGracePeriodSeconds) $required -}}{{ fail (printf "terminationGracePeriodSeconds must exceed drain propagation + timeout + 5s close (%ds)" $required) }}{{- end -}}
{{- end -}}

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
