{{- define "mecak8s.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- define "mecak8s.fullname" -}}
{{- if .Values.fullnameOverride }}{{ .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}{{ else }}{{ printf "%s-%s" .Release.Name (include "mecak8s.name" .) | trunc 63 | trimSuffix "-" }}{{ end }}
{{- end }}
{{- define "mecak8s.labels" -}}
app.kubernetes.io/name: {{ include "mecak8s.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/part-of: mecak8s
{{- end }}
{{- define "mecak8s.selectorLabels" -}}
app.kubernetes.io/name: {{ include "mecak8s.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: agent
{{- end }}
{{- define "mecak8s.validateImage" -}}
{{- required "image.repository is required" .Values.image.repository -}}
{{- if and .Values.image.digest .Values.image.tag -}}
{{- fail "set exactly one of image.digest or image.tag" -}}
{{- end -}}
{{- if and (not .Values.image.digest) (not .Values.image.tag) -}}
{{- fail "set one of image.digest or image.tag" -}}
{{- end -}}
{{- end -}}
{{- define "mecak8s.redisPort" -}}
{{- $match := regexFind ":[0-9]+$" .Values.redis.endpoint -}}
{{- if eq $match "" -}}
{{- fail "redis.endpoint must end in a numeric port" -}}
{{- end -}}
{{- $port := trimPrefix ":" $match -}}
{{- if or (lt (int $port) 1) (gt (int $port) 65535) -}}
{{- fail "redis.endpoint port must be between 1 and 65535" -}}
{{- end -}}
{{- $port -}}
{{- end -}}
{{/*
mecak8s.redisSecretMounted is non-empty when the external profile has at least one
Secret key to project. An empty redis.caKey selects system-trust TLS
(--redis-tls), which needs no mounted CA, so a CA-only-by-system-trust install
with no ACL mounts no Secret at all.
*/}}
{{- define "mecak8s.redisSecretMounted" -}}
{{- if not .Values.redis.local.enabled -}}
{{- if or .Values.redis.caKey .Values.redis.passwordKey .Values.redis.usernameKey -}}
mounted
{{- end -}}
{{- end -}}
{{- end -}}
{{- define "mecak8s.validateRedis" -}}
{{- if not .Values.redis.local.enabled -}}
{{- required "redis.endpoint is required when redis.local.enabled is false" .Values.redis.endpoint -}}
{{- include "mecak8s.redisPort" . -}}
{{- if and (include "mecak8s.redisSecretMounted" .) (not .Values.redis.credentialsSecret) -}}
{{- fail "redis.credentialsSecret is required when any of redis.caKey/passwordKey/usernameKey is set" -}}
{{- end -}}
{{- if and .Values.redis.usernameKey (not .Values.redis.passwordKey) -}}
{{- fail "redis.usernameKey requires redis.passwordKey" -}}
{{- end -}}
{{- end -}}
{{- end -}}
