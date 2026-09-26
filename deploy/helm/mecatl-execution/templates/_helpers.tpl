{{- define "mecatl-execution.fullname" -}}
{{- default (printf "%s-mecatl-execution" .Release.Name) .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/* Retained resources are adoptable only by their original Helm identity. */}}
{{- define "mecatl-execution.retainedMetadata" -}}
{{- $live := lookup .apiVersion .kind .root.Release.Namespace .name -}}
{{- if $live -}}
{{- if or $live.metadata.deletionTimestamp (ne (dig "app.kubernetes.io/managed-by" "" ($live.metadata.labels | default dict)) "Helm") (ne (dig "meta.helm.sh/release-name" "" ($live.metadata.annotations | default dict)) .root.Release.Name) (ne (dig "meta.helm.sh/release-namespace" "" ($live.metadata.annotations | default dict)) .root.Release.Namespace) -}}
{{- fail (printf "retained %s %s has foreign or ambiguous Helm ownership" .kind .name) -}}
{{- end -}}
{{- end -}}
name: {{ .name }}
namespace: {{ .root.Release.Namespace }}
labels:
  app.kubernetes.io/managed-by: Helm
annotations:
  helm.sh/resource-policy: keep
  meta.helm.sh/release-name: {{ .root.Release.Name | quote }}
  meta.helm.sh/release-namespace: {{ .root.Release.Namespace | quote }}
{{- end -}}

{{/* Frozen workload identity prevents orphan allow policies from widening egress. */}}
{{- define "mecatl-execution.lifetimeConfig" -}}
{{- dict "profiles" .Values.profiles "networkPolicy" .Values.networkPolicy "securitySecretName" .Values.provider.securitySecretName | toJson -}}
{{- end -}}
