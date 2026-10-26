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
{{/*
The authority (host and optional port) of an http(s) URL.
*/}}
{{- define "mecatl-studio.urlAuthority" -}}
{{- regexReplaceAll "(?i)^https?://([^/]*).*$" . "${1}" -}}
{{- end }}
{{/*
The destination port of an http(s) URL: its explicit port, else the scheme's.
*/}}
{{- define "mecatl-studio.urlPort" -}}
{{- $port := regexFind ":[0-9]+$" (include "mecatl-studio.urlAuthority" .) | trimPrefix ":" -}}
{{- if $port }}{{ atoi $port }}{{ else if regexMatch "(?i)^https://" . }}443{{ else }}80{{ end -}}
{{- end }}
{{- define "mecatl-studio.publicHost" -}}
{{- regexReplaceAll ":[0-9]+$" (include "mecatl-studio.urlAuthority" .Values.publicURL) "" | lower -}}
{{- end }}
{{/*
Non-empty when the publicURL host is an IPv4 or bracketed IPv6 address.
*/}}
{{- define "mecatl-studio.publicHostIsIP" -}}
{{- if regexMatch "^([0-9]+\\.){3}[0-9]+$|^\\[" (include "mecatl-studio.publicHost" .) }}true{{ end -}}
{{- end }}
{{/*
Fail at render time on settings the Studio image refuses at startup and on the
chart's own invariants, so a misconfigured release never reaches the cluster.
Image checks the chart cannot see, such as the session secret's length, still
fail at startup.
*/}}
{{- define "mecatl-studio.validate" -}}
{{- if and .Values.image.digest .Values.image.tag -}}
{{- fail "set at most one of image.digest or image.tag" -}}
{{- end -}}
{{- /* An http(s) scheme, a host name or bracketed IPv6 address, and an optional port. */ -}}
{{- $origin := "(?i)^https?://([^/?#@:\\[\\][:space:]]+|\\[[0-9a-f:.]+\\])(:[0-9]{1,5})?" -}}
{{- $baseURL := required "mecatl.baseURL is required: the http(s) address of the Mecatl gRPC listener" .Values.mecatl.baseURL -}}
{{- if not (regexMatch (printf "%s/?$" $origin) $baseURL) -}}
{{- fail "mecatl.baseURL must be an http or https scheme, host, and optional port, without credentials, path, query, or fragment" -}}
{{- end -}}
{{- if and .Values.mecatl.resourceURL (not (regexMatch (printf "%s(/[^?#[:space:]]*)?$" $origin) .Values.mecatl.resourceURL)) -}}
{{- fail "mecatl.resourceURL must be an http or https URL without credentials, query, or fragment" -}}
{{- end -}}
{{- $publicURL := required "publicURL is required: the browser-facing origin, for example https://studio.example.com" .Values.publicURL -}}
{{- if not (regexMatch (printf "%s/?$" $origin) $publicURL) -}}
{{- fail "publicURL must be an http or https scheme, host, and optional port, without credentials, path, query, or fragment" -}}
{{- end -}}
{{- range $name, $url := dict "mecatl.baseURL" $baseURL "mecatl.resourceURL" .Values.mecatl.resourceURL "publicURL" $publicURL -}}
{{- if $url -}}
{{- $port := int (include "mecatl-studio.urlPort" $url) -}}
{{- if or (lt $port 1) (gt $port 65535) -}}
{{- fail (printf "%s has port %d; ports run from 1 to 65535" $name $port) -}}
{{- end -}}
{{- end -}}
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
{{- if and (ne .Values.service.type "ClusterIP") (gt (int .Values.trustedProxyHops) 0) -}}
{{- fail (printf "trustedProxyHops must be 0 with a %s Service: it adds no X-Forwarded-For entry, so clients would choose the address Studio rate-limits and audits" .Values.service.type) -}}
{{- end -}}
{{- if .Values.httpRoute.enabled -}}
{{- if eq (len .Values.httpRoute.parentRefs) 0 -}}
{{- fail "httpRoute.parentRefs is required when httpRoute.enabled is true" -}}
{{- end -}}
{{- $host := include "mecatl-studio.publicHost" . -}}
{{- if include "mecatl-studio.publicHostIsIP" . -}}
{{- if .Values.httpRoute.hostnames -}}
{{- fail "httpRoute.hostnames must be empty when publicURL uses an IP address: browsers send that address as the Host, which no route hostname can match" -}}
{{- end -}}
{{- else if .Values.httpRoute.hostnames -}}
{{- $matched := false -}}
{{- range $hostname := .Values.httpRoute.hostnames -}}
{{- if or (eq $hostname $host) (and (hasPrefix "*." $hostname) (hasSuffix (trimPrefix "*" $hostname) $host)) -}}
{{- $matched = true -}}
{{- end -}}
{{- end -}}
{{- if not $matched -}}
{{- fail (printf "httpRoute.hostnames must include %s, the publicURL host: Studio answers only that Host" $host) -}}
{{- end -}}
{{- else if not (regexMatch "^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$" $host) -}}
{{- fail (printf "publicURL host %q is not a valid route hostname; set httpRoute.hostnames" $host) -}}
{{- end -}}
{{- end -}}
{{- range $label := list "app.kubernetes.io/name" "app.kubernetes.io/instance" -}}
{{- if hasKey $.Values.podLabels $label -}}
{{- fail (printf "podLabels must not set the selector label %s" $label) -}}
{{- end -}}
{{- end -}}
{{- $owned := list "MECATL_BASE_URL" "MECATL_RESOURCE_URL" "MECATL_AUTH_TOKEN" "MECATL_DEV_MOCK" "STUDIO_PUBLIC_URL" "STUDIO_OIDC_CLIENT_ID" "STUDIO_SESSION_SECRET" "STUDIO_ALLOW_UNAUTHENTICATED" "STUDIO_TRUSTED_PROXY_HOPS" "STUDIO_LOG_LEVEL" "STUDIO_HOST" "STUDIO_PORT" "STUDIO_IMAGE" "STUDIO_WEB_DIST" "NODE_ENV" -}}
{{- range $env := .Values.extraEnv -}}
{{- if and (hasKey $env "name") (has $env.name $owned) -}}
{{- fail (printf "extraEnv name %q collides with a variable the chart or image owns" $env.name) -}}
{{- end -}}
{{- if eq $env.name "NODE_TLS_REJECT_UNAUTHORIZED" -}}
{{- fail "extraEnv must not set NODE_TLS_REJECT_UNAUTHORIZED: it disables certificate checks for the tokens Studio sends; trust a private CA with NODE_EXTRA_CA_CERTS instead" -}}
{{- end -}}
{{- end -}}
{{- end }}
