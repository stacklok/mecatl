{{- define "mecatl-execution.fullname" -}}
{{- default (printf "%s-mecatl-execution" .Release.Name) .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}
