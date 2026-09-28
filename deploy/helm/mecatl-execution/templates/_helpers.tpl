{{- define "mecatl-execution.fullname" -}}
{{- default (printf "%s-mecatl-execution" .Release.Name) .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "mecatl-execution.executorServiceAccount" -}}
{{- $fullname := include "mecatl-execution.fullname" . -}}
{{- if le (len $fullname) 54 -}}
{{- printf "%s-executor" $fullname -}}
{{- else -}}
{{- printf "%s-%s-executor" ($fullname | trunc 45 | trimSuffix "-") ($fullname | sha256sum | trunc 8) -}}
{{- end -}}
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

{{/* Validate the opt-in projection before rendering; never include manifest contents in errors. */}}
{{- define "mecatl-execution.securitySources" -}}
{{- $sources := .Values.provider.securitySources | default list -}}
{{- if $sources -}}
{{- if .Values.provider.securitySecretName -}}
{{- fail "provider.securitySources cannot be combined with securitySecretName" -}}
{{- end -}}
{{- $names := dict -}}
{{- $files := dict -}}
{{- $count := 0 -}}
{{- range $source := $sources -}}
{{- if hasKey $names $source.name -}}
{{- fail "provider.securitySources has duplicate source names" -}}
{{- end -}}
{{- $_ := set $names $source.name true -}}
{{- $keys := dict -}}
{{- range $item := $source.items -}}
{{- $count = add1 $count -}}
{{- if gt $count 32 -}}
{{- fail "provider.securitySources exceeds 32 mappings" -}}
{{- end -}}
{{- if hasKey $keys $item.key -}}
{{- fail "provider.securitySources has duplicate keys in a source" -}}
{{- end -}}
{{- $_ := set $keys $item.key true -}}
{{- if or (eq $item.path "manifest.json") (hasKey $files $item.path) -}}
{{- fail "provider.securitySources has a duplicate or reserved destination file" -}}
{{- end -}}
{{- $_ := set $files $item.path true -}}
{{- end -}}
{{- end -}}
{{- $manifest := .Values.provider.securityManifest | fromJson -}}
{{- if or (not (kindIs "map" $manifest)) (not (kindIs "slice" (get $manifest "keys"))) (not (kindIs "map" (get $manifest "tls"))) -}}
{{- fail "provider.securityManifest requires keys and tls file references with securitySources" -}}
{{- end -}}
{{- $refs := list -}}
{{- range $key := get $manifest "keys" -}}
{{- if not (kindIs "map" $key) -}}
{{- fail "provider.securityManifest has invalid key file references" -}}
{{- end -}}
{{- $refs = append $refs (get $key "file") -}}
{{- end -}}
{{- if not $refs -}}
{{- fail "provider.securityManifest requires key file references with securitySources" -}}
{{- end -}}
{{- $tls := get $manifest "tls" -}}
{{- range $field := list "certificateFile" "privateKeyFile" "clientCAFile" -}}
{{- $refs = append $refs (get $tls $field) -}}
{{- end -}}
{{- range $file := $refs -}}
{{- if or (not (kindIs "string" $file)) (not (hasKey $files $file)) -}}
{{- fail "provider.securityManifest references an unmapped security file" -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}
