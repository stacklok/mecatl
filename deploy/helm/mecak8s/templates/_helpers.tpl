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
{{- $_ := required "image.repository is required" .Values.image.repository -}}
{{- if and .Values.image.digest .Values.image.tag -}}
{{- fail "set at most one of image.digest or image.tag" -}}
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
{{- $_ := required "redis.endpoint is required when redis.local.enabled is false" .Values.redis.endpoint -}}
{{- $_ := include "mecak8s.redisPort" . -}}
{{- if and (include "mecak8s.redisSecretMounted" .) (not .Values.redis.credentialsSecret) -}}
{{- fail "redis.credentialsSecret is required when any of redis.caKey/passwordKey/usernameKey is set" -}}
{{- end -}}
{{- if and .Values.redis.usernameKey (not .Values.redis.passwordKey) -}}
{{- fail "redis.usernameKey requires redis.passwordKey" -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- define "mecak8s.validateOIDC" -}}
{{- if .Values.oidc.enabled -}}
{{- $_ := required "oidc.issuer is required when oidc.enabled is true" .Values.oidc.issuer -}}
{{- $_ := required "oidc.audience is required when oidc.enabled is true" .Values.oidc.audience -}}
{{- end -}}
{{- if .Values.oidc.allowPrivateHTTPSIssuer -}}
{{- if not .Values.oidc.enabled -}}
{{- fail "oidc.allowPrivateHTTPSIssuer requires oidc.enabled" -}}
{{- end -}}
{{- if not (hasPrefix "https://" .Values.oidc.issuer) -}}
{{- fail "oidc.allowPrivateHTTPSIssuer requires an https:// oidc.issuer" -}}
{{- end -}}
{{- $_ := required "oidc.caSecret is required when oidc.allowPrivateHTTPSIssuer is true" .Values.oidc.caSecret -}}
{{- $_ := required "oidc.caKey is required when oidc.allowPrivateHTTPSIssuer is true" .Values.oidc.caKey -}}
{{- end -}}
{{- if or .Values.oidc.caSecret .Values.oidc.caKey -}}
{{- $_ := required "oidc.caSecret is required when oidc.caKey is set" .Values.oidc.caSecret -}}
{{- $_ := required "oidc.caKey is required when oidc.caSecret is set" .Values.oidc.caKey -}}
{{- end -}}
{{- end -}}
{{- define "mecak8s.validateTLS" -}}
{{- if .Values.tls.enabled -}}
{{- $_ := required "tls.secretName is required when tls.enabled is true" .Values.tls.secretName -}}
{{- $_ := required "tls.certKey is required when tls.enabled is true" .Values.tls.certKey -}}
{{- $_ := required "tls.keyKey is required when tls.enabled is true" .Values.tls.keyKey -}}
{{- end -}}
{{- end -}}
{{- define "mecak8s.validateProviderSecurity" -}}
{{- if and .Values.mockProvider .Values.security.tlsTerminatedUpstream -}}
{{- fail "security.tlsTerminatedUpstream applies only to a real provider; it is ignored when mockProvider=true, so setting both is a mistake" -}}
{{- end -}}
{{- if and (not .Values.mockProvider) (not .Values.security.allowUnsafeRealProvider) -}}
{{- if not .Values.oidc.enabled -}}
{{- fail "mockProvider=false requires oidc.enabled=true (OIDC authenticates callers); set security.allowUnsafeRealProvider=true only for local or trusted-mesh deployments" -}}
{{- end -}}
{{- if and (not .Values.tls.enabled) (not .Values.security.tlsTerminatedUpstream) -}}
{{- fail "mockProvider=false requires tls.enabled=true (TLS protects transport); set security.allowUnsafeRealProvider=true only for local or trusted-mesh deployments" -}}
{{- end -}}
{{- if and (not .Values.tls.enabled) .Values.security.tlsTerminatedUpstream (ne .Values.service.type "ClusterIP") -}}
{{- fail "security.tlsTerminatedUpstream=true with tls.enabled=false requires service.type=ClusterIP" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/* Validate cross-entry MCP invariants that JSON Schema cannot express. */}}
{{- define "mecak8s.validateMCP" -}}
{{- $seen := dict -}}
{{- $ownedEnv := dict -}}
{{- range $server := .Values.mcp.servers -}}
{{- $folded := lower $server.name -}}
{{- if hasKey $seen $folded -}}{{ fail (printf "mcp.servers name %q is duplicated case-insensitively" $server.name) }}{{- end -}}
{{- $_ := set $seen $folded true -}}
{{- $envBase := upper $server.name -}}
{{- if and $server.insecureHTTP (eq $server.auth.mode "oauth") -}}{{ fail (printf "mcp.servers[%s].insecureHTTP is invalid for oauth" $server.name) }}{{- end -}}
{{- if eq $server.auth.mode "staticBearer" -}}
{{- $_ := set $ownedEnv (printf "MCP_%s_TOKEN" $envBase) true -}}
{{- end -}}
{{- if eq $server.auth.mode "oauth" -}}
{{- range $field, $value := dict "profile" $server.auth.oauth.profile "principal" $server.auth.oauth.principal -}}
{{- if or (eq (trim $value) "") (regexMatch "[\x00-\x1f\x7f]" $value) -}}{{ fail (printf "mcp.servers[%s].auth.oauth.%s must be non-blank and contain no control characters" $server.name $field) }}{{- end -}}
{{- end -}}
{{- range $scope := $server.auth.oauth.scopes -}}{{- if or (eq (trim $scope) "") (regexMatch "[\x00-\x1f\x7f]" $scope) -}}{{ fail (printf "mcp.servers[%s].auth.oauth.scopes must be non-blank and contain no control characters" $server.name) }}{{- end -}}{{- end -}}
{{- if eq $server.auth.oauth.client.mode "preregistered" -}}
{{- $clientID := $server.auth.oauth.client.preregistered.id -}}
{{- if or (eq (trim $clientID) "") (regexMatch "[\x00-\x1f\x7f]" $clientID) -}}{{ fail (printf "mcp.servers[%s].auth.oauth.client.preregistered.id must be non-blank and contain no control characters" $server.name) }}{{- end -}}
{{- $_ := set $ownedEnv (printf "MECATL_MCP_%s_CLIENT_SECRET" $envBase) true -}}
{{- end -}}
{{- $_ := set $ownedEnv (printf "MECATL_MCP_%s_CREDENTIAL" $envBase) true -}}
{{- end -}}
{{- end -}}
{{- range $env := .Values.extraEnv -}}
{{- if and (hasKey $env "name") (hasKey $ownedEnv $env.name) -}}{{ fail (printf "extraEnv name %q collides with an MCP authentication environment variable owned by the chart" $env.name) }}{{- end -}}
{{- end -}}
{{- end -}}

{{/*
Strict runtime operator profile. mecak8s hardcodes its MCP authority default to
broker mode (cmd/mecak8s/flags.go); with zero mcp.servers configured that still
demands a verified caller identity for no protective benefit (ADR 0289), so an
empty server list explicitly opts back into global mode here. A non-empty list
renders the OAuth broker profiles as before (only oauth entries are broker
material; none/staticBearer servers ride the legacy --mcp-server flag instead).
*/}}
{{- define "mecak8s.mcpOAuthSettings" -}}
{{- if not .Values.mcp.servers -}}
mcp:
  mode: global
{{- else }}
mcp:
  servers:
{{- range $server := .Values.mcp.servers }}
{{- if eq $server.auth.mode "oauth" }}
    - name: {{ $server.name | quote }}
      url: {{ $server.url | quote }}
      auth:
        mode: oauth
        oauth:
          profile: {{ $server.auth.oauth.profile | quote }}
          principal: {{ $server.auth.oauth.principal | quote }}
          issuer: {{ $server.auth.oauth.issuer | quote }}
          client:
            mode: {{ $server.auth.oauth.client.mode }}
{{- if eq $server.auth.oauth.client.mode "preregistered" }}
            preregistered:
              id: {{ $server.auth.oauth.client.preregistered.id | quote }}
              secret_env: {{ printf "MECATL_MCP_%s_CLIENT_SECRET" (upper $server.name) }}
{{- else }}
            cimd:
              document_url: {{ $server.auth.oauth.client.cimd.documentURL | quote }}
{{- end }}
          scopes: {{ toJson $server.auth.oauth.scopes }}
          request_refresh_token: {{ default false $server.auth.oauth.requestRefreshToken }}
          credentials:
            mode: environment
            environment:
              credential_env: {{ printf "MECATL_MCP_%s_CREDENTIAL" (upper $server.name) }}
              allow_process_local_refresh: {{ default false $server.auth.oauth.credentials.allowProcessLocalRefresh }}
          network:
            additional_origins: {{ toJson $server.auth.oauth.network.additionalOrigins }}
            private_origins: {{ toJson $server.auth.oauth.network.privateOrigins }}
            max_redirects: {{ $server.auth.oauth.network.maxRedirects }}
{{- end }}
{{- end }}
{{- end }}
{{- end -}}
