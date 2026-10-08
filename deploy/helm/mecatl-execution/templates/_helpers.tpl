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
{{- $definitions := dict -}}
{{- $total := 0 -}}
{{- range $id, $template := .Values.templates -}}
{{- if hasKey $template "default" -}}
{{- $selected := get $template.revisions $template.default | default dict -}}
{{- if or (not (hasKey $template.revisions $template.default)) ($selected.deprecated | default false) ($selected.revoked | default false) -}}
{{- fail (printf "execution template %s requires an eligible default revision" $id) -}}
{{- end -}}
{{- else -}}
{{- range $revision, $entry := $template.revisions -}}
{{- if not (or ($entry.deprecated | default false) ($entry.revoked | default false)) -}}
{{- fail (printf "execution template %s has selectable revisions but no default" $id) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- $revisions := dict -}}
{{- range $revision, $definition := $template.revisions -}}
{{- range $namespace, $fields := dig "display" "extensions" (dict) $definition -}}
{{- range $field, $_ := $fields -}}
{{- if gt (len (printf "%s/%s" $namespace $field)) 253 -}}
{{- fail "execution template display metadata key exceeds 253 bytes" -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- $total = add1 $total -}}
{{- if gt $total 64 -}}
{{- fail "execution template inventory exceeds 64 revisions" -}}
{{- end -}}
{{- $_ := set $revisions $revision $definition.execution -}}
{{- end -}}
{{- $_ := set $definitions $id $revisions -}}
{{- end -}}
{{- dict "definitions" $definitions "networkPolicy" .Values.networkPolicy | toJson -}}
{{- end -}}

{{/* Validate policy shape and opt-in projection without echoing manifest contents. */}}
{{- define "mecatl-execution.securitySources" -}}
{{- $manifest := .Values.provider.securityManifest | fromJson -}}
{{- if or (not (kindIs "map" $manifest)) (ne (len $manifest) 4) (not (kindIs "map" (get $manifest "tls"))) (not (kindIs "slice" (get $manifest "clients"))) (ne (get $manifest "version") (float64 1)) (lt (int (get $manifest "generation")) 1) -}}
{{- fail "provider.securityManifest requires version, generation, tls and clients only" -}}
{{- end -}}
{{- $tls := get $manifest "tls" -}}
{{- if ne (len $tls) 3 -}}
{{- fail "provider.securityManifest tls requires only certificateFile, privateKeyFile and clientCAFile" -}}
{{- end -}}
{{- $refs := list -}}
{{- range $field := list "certificateFile" "privateKeyFile" "clientCAFile" -}}
{{- $refs = append $refs (get $tls $field) -}}
{{- end -}}
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
{{- range $file := $refs -}}
{{- if or (not (kindIs "string" $file)) (not (hasKey $files $file)) -}}
{{- fail "provider.securityManifest references an unmapped security file" -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}
