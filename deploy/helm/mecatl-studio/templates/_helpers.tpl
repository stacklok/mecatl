{{- define "mecatl-studio.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}
{{/*
A release named after the chart (the common case, and the name the raw
manifests this chart replaces used) keeps that name instead of doubling it.
*/}}
{{- define "mecatl-studio.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := include "mecatl-studio.name" . }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}
{{- define "mecatl-studio.selectorLabels" -}}
app.kubernetes.io/name: {{ include "mecatl-studio.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}
{{- define "mecatl-studio.labels" -}}
{{ include "mecatl-studio.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | replace "+" "_" | trunc 63 | trimSuffix "-" | quote }}
app.kubernetes.io/part-of: mecatl
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- define "mecatl-studio.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "mecatl-studio.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- required "serviceAccount.name is required when serviceAccount.create is false" .Values.serviceAccount.name }}
{{- end }}
{{- end }}
{{- define "mecatl-studio.image" -}}
{{- if .Values.image.digest -}}
{{ .Values.image.repository }}@{{ .Values.image.digest }}
{{- else -}}
{{ .Values.image.repository }}:{{ default .Chart.AppVersion .Values.image.tag }}
{{- end -}}
{{- end }}
{{- define "mecatl-studio.publicHost" -}}
{{- regexReplaceAll "^https?://([^/:]+)(:[0-9]+)?$" .Values.publicURL "${1}" | lower -}}
{{- end }}
{{/*
The destination port of an http(s) URL: its explicit port, else the scheme's.
*/}}
{{- define "mecatl-studio.urlPort" -}}
{{- $port := regexFind ":[0-9]+(/|$)" (regexReplaceAll "^https?://" . "") | trimPrefix ":" | trimSuffix "/" -}}
{{- if $port }}{{ $port }}{{ else if hasPrefix "https://" . }}443{{ else }}80{{ end -}}
{{- end }}
{{/*
Fail at render time on configurations the Studio image refuses at startup, so a
misconfigured release never reaches the cluster.
*/}}
{{- define "mecatl-studio.validate" -}}
{{- if and .Values.image.digest .Values.image.tag -}}
{{- fail "set at most one of image.digest or image.tag" -}}
{{- end -}}
{{- $baseURL := required "mecatl.baseURL is required: the https:// address of the Mecatl gRPC listener" .Values.mecatl.baseURL -}}
{{- if not (regexMatch "^https?://[^/?#@[:space:]]+/?$" $baseURL) -}}
{{- fail "mecatl.baseURL must be an http or https scheme, host, and optional port, without credentials, path, query, or fragment" -}}
{{- end -}}
{{- if and .Values.mecatl.resourceURL (not (regexMatch "^https?://[^/?#@[:space:]]+(/[^?#[:space:]]*)?$" .Values.mecatl.resourceURL)) -}}
{{- fail "mecatl.resourceURL must be an http or https URL without credentials, query, or fragment" -}}
{{- end -}}
{{- $publicURL := required "publicURL is required: the browser-facing origin, for example https://studio.example.com" .Values.publicURL -}}
{{- if not (regexMatch "^https?://[^/?#@[:space:]]+$" $publicURL) -}}
{{- fail "publicURL must be an http or https origin without a path" -}}
{{- end -}}
{{- if and (not .Values.allowUnauthenticated) (not .Values.session.secretName) -}}
{{- fail "session.secretName is required for browser login; set allowUnauthenticated=true only for a static-token or no-authentication target" -}}
{{- end -}}
{{- if and .Values.session.secretName (not .Values.session.secretKey) -}}
{{- fail "session.secretKey is required when session.secretName is set" -}}
{{- end -}}
{{- if .Values.mecatl.authToken.secretName -}}
{{- if not .Values.mecatl.authToken.secretKey -}}
{{- fail "mecatl.authToken.secretKey is required when mecatl.authToken.secretName is set" -}}
{{- end -}}
{{- if not .Values.allowUnauthenticated -}}
{{- fail "mecatl.authToken disables browser login, so every browser acts as one principal; it requires allowUnauthenticated=true" -}}
{{- end -}}
{{- end -}}
{{- if .Values.httpRoute.enabled -}}
{{- if eq (len .Values.httpRoute.parentRefs) 0 -}}
{{- fail "httpRoute.parentRefs is required when httpRoute.enabled is true" -}}
{{- end -}}
{{- if and (not .Values.httpRoute.hostnames) (regexMatch "^([0-9.]+|.*[\\[:].*)$" (include "mecatl-studio.publicHost" .)) -}}
{{- fail "httpRoute.hostnames is required when publicURL uses an IP address" -}}
{{- end -}}
{{- end -}}
{{- range $label := list "app.kubernetes.io/name" "app.kubernetes.io/instance" -}}
{{- if hasKey $.Values.podLabels $label -}}
{{- fail (printf "podLabels must not set the selector label %s" $label) -}}
{{- end -}}
{{- end -}}
{{- $owned := list "MECATL_BASE_URL" "MECATL_RESOURCE_URL" "MECATL_AUTH_TOKEN" "STUDIO_PUBLIC_URL" "STUDIO_SESSION_SECRET" "STUDIO_ALLOW_UNAUTHENTICATED" "STUDIO_TRUSTED_PROXY_HOPS" "STUDIO_LOG_LEVEL" "STUDIO_HOST" "STUDIO_PORT" "STUDIO_IMAGE" "STUDIO_WEB_DIST" -}}
{{- range $env := .Values.extraEnv -}}
{{- if and (hasKey $env "name") (has $env.name $owned) -}}
{{- fail (printf "extraEnv name %q collides with a variable the chart or image owns" $env.name) -}}
{{- end -}}
{{- end -}}
{{- end }}
