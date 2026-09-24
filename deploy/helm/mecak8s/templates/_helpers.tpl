{{- define "mecak8s.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "mecak8s.brokerFullname" -}}
{{- printf "%s-broker" (include "mecak8s.fullname" . | trunc 49 | trimSuffix "-") | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- define "mecak8s.brokerServerName" -}}
{{- default (printf "%s.%s.svc" (include "mecak8s.brokerFullname" .) .Release.Namespace) .Values.broker.clientCA.serverName -}}
{{- end -}}
{{- define "mecak8s.brokerLabels" -}}
app.kubernetes.io/name: mecabroker
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: broker
{{- end -}}
{{- define "mecak8s.brokerBootstrapRBACName" -}}
{{- printf "%s-kubernetes-discovery-%s" (include "mecak8s.brokerFullname" . | trunc 28 | trimSuffix "-") (printf "%s/%s" .Release.Namespace .Release.Name | sha256sum | trunc 12) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "mecak8s.fullname" -}}
{{- if .Values.fullnameOverride }}{{ .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}{{ else }}{{ printf "%s-%s" .Release.Name (include "mecak8s.name" .) | trunc 63 | trimSuffix "-" }}{{ end }}
{{- end }}
{{- define "mecak8s.telemetryConfigMapName" -}}
{{- printf "%s-telemetry" (include "mecak8s.fullname" . | trunc 53 | trimSuffix "-") | trunc 63 | trimSuffix "-" -}}
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
{{- if and .Values.image.digest .Values.image.tag -}}{{ fail "set at most one of image.digest or image.tag" }}{{- end -}}
{{- if and .Values.image.digest (not (regexMatch "^sha256:[0-9a-f]{64}$" .Values.image.digest)) -}}{{ fail "image.digest must be a lowercase sha256 digest" }}{{- end -}}
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
{{- if gt (int .Values.redis.follow.maxFollowers) (int .Values.redis.follow.poolSize) -}}
{{- fail "redis.follow.maxFollowers must not exceed redis.follow.poolSize" -}}
{{- end -}}
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
{{- $profileSet := or .Values.oidc.resource .Values.oidc.clientID (gt (len .Values.oidc.scopes) 0) -}}
{{- if $profileSet -}}
{{- if not .Values.oidc.enabled -}}{{ fail "oidc protected-resource profile requires oidc.enabled=true" }}{{- end -}}
{{- if not (regexMatch "^https://[^/?#[:space:]]+" .Values.oidc.issuer) -}}{{ fail "oidc protected-resource profile requires an https:// oidc.issuer with a non-empty host" }}{{- end -}}
{{- if not .Values.oidc.resource -}}{{ fail "oidc.resource is required when protected-resource profile is set" }}{{- end -}}
{{- if not .Values.oidc.clientID -}}{{ fail "oidc.clientID is required when protected-resource profile is set" }}{{- end -}}
{{- if or (gt (len .Values.oidc.resource) 1024) (regexMatch "[\\x00-\\x1f\\x7f-\\x9f]" .Values.oidc.resource) (regexMatch "[\\p{Cf}]" .Values.oidc.resource) (not (regexMatch "^https://[^/?#[:space:]]+(/[^?#[:space:]]*)?$" .Values.oidc.resource)) (contains "@" .Values.oidc.resource) (contains "," .Values.oidc.resource) (contains "\"" .Values.oidc.resource) (contains "\\" .Values.oidc.resource) (regexMatch "%[^0-9A-Fa-f]|%[0-9A-Fa-f][^0-9A-Fa-f]|%[0-9A-Fa-f]$" .Values.oidc.resource) (regexMatch "(^|/|%2[fF])(\\.|\\.\\.|%2[eE]|%2[eE]%2[eE]|\\.%2[eE]|%2[eE]\\.)(/|%2[fF]|$)" .Values.oidc.resource) (regexMatch "^https://:" .Values.oidc.resource) (regexMatch "^https://[^/?#:\\[]+:([^0-9/]|$)" .Values.oidc.resource) (regexMatch "^https://[^/?#:\\[]+:[0-9]+[^0-9/]" .Values.oidc.resource) (regexMatch "^https://\\[[^\\]/?#]+\\]:([^0-9/]|$)" .Values.oidc.resource) (regexMatch "^https://\\[[^\\]/?#]+\\]:[0-9]+[^0-9/]" .Values.oidc.resource) -}}{{ fail "oidc.resource must be an absolute HTTPS URL of at most 1024 bytes without credentials, query, fragment, commas, quotes, backslashes, malformed escapes, dot segments, invalid authority, control, or Unicode format characters" }}{{- end -}}
{{- $resourcePort := regexFind "^https://[^/?#:\\[]+:[0-9]+" .Values.oidc.resource -}}
{{- if $resourcePort -}}
{{- $port := regexFind ":[0-9]+" $resourcePort | trimPrefix ":" | int -}}
{{- if or (lt $port 1) (gt $port 65535) -}}{{ fail "oidc.resource port must be between 1 and 65535" }}{{- end -}}
{{- end -}}
{{- $resourceIPv6Port := regexFind "^https://\\[[^\\]/?#]+\\]:[0-9]+" .Values.oidc.resource -}}
{{- if $resourceIPv6Port -}}
{{- $port6 := regexFind "\\]:[0-9]+$" $resourceIPv6Port | trimPrefix "]:" | int -}}
{{- if or (lt $port6 1) (gt $port6 65535) -}}{{ fail "oidc.resource port must be between 1 and 65535" }}{{- end -}}
{{- end -}}
{{- if or (gt (len .Values.oidc.clientID) 1024) (regexMatch "[\\x00-\\x1f\\x7f-\\x9f]" .Values.oidc.clientID) (regexMatch "[\\p{Cf}]" .Values.oidc.clientID) -}}{{ fail "oidc.clientID must be non-empty, at most 1024 bytes, and contain no control or Unicode format characters" }}{{- end -}}
{{- range $scope := .Values.oidc.scopes -}}
{{- if or (eq (trim $scope) "") (not (regexMatch "^[!-~]+$" $scope)) (contains "," $scope) (contains "\"" $scope) (contains "\\" $scope) -}}{{ fail (printf "oidc.scopes entry %q is invalid" $scope) }}{{- end -}}
{{- end -}}
{{- end -}}
{{- if .Values.oidc.enabled -}}
{{- $_ := required "oidc.issuer is required when oidc.enabled is true" .Values.oidc.issuer -}}
{{- $_ := required "oidc.audience is required when oidc.enabled is true" .Values.oidc.audience -}}
{{- end -}}
{{- if .Values.oidc.allowPrivateHTTPSIssuer -}}
{{- if not .Values.oidc.enabled -}}{{ fail "oidc.allowPrivateHTTPSIssuer requires oidc.enabled" }}{{- end -}}
{{- if not (regexMatch "^https://[^/?#[:space:]]+" .Values.oidc.issuer) -}}{{ fail "oidc.allowPrivateHTTPSIssuer requires an https:// oidc.issuer with a non-empty host" }}{{- end -}}
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
{{- define "mecak8s.validateLearningStore" -}}
{{- $store := .Values.learning.store -}}
{{- $tls := $store.tls -}}
{{- if or $store.tokenSecret $store.tokenKey -}}
{{- $_ := required "learning.store.tokenSecret is required when learning.store.tokenKey is set" $store.tokenSecret -}}
{{- $_ := required "learning.store.tokenKey is required when learning.store.tokenSecret is set" $store.tokenKey -}}
{{- end -}}
{{- $hasCA := or $tls.caSecret $tls.caKey -}}
{{- $hasMTLS := or $tls.mtlsSecret $tls.certKey $tls.keyKey -}}
{{- if or $hasCA $hasMTLS -}}
{{- if not $tls.enabled -}}{{ fail "learning.store TLS material requires learning.store.tls.enabled=true" }}{{- end -}}
{{- end -}}
{{- if $hasCA -}}
{{- $_ := required "learning.store.tls.caSecret is required when learning.store.tls.caKey is set" $tls.caSecret -}}
{{- $_ := required "learning.store.tls.caKey is required when learning.store.tls.caSecret is set" $tls.caKey -}}
{{- end -}}
{{- if $hasMTLS -}}
{{- $_ := required "learning.store.tls.mtlsSecret is required when mTLS material is set" $tls.mtlsSecret -}}
{{- $_ := required "learning.store.tls.certKey is required when mTLS material is set" $tls.certKey -}}
{{- $_ := required "learning.store.tls.keyKey is required when mTLS material is set" $tls.keyKey -}}
{{- end -}}
{{- if and $store.endpoint $store.tokenSecret $store.tokenKey (not $tls.enabled) (not (regexMatch "^(localhost|127(\\.[0-9]{1,3}){3}|\\[::1\\]):[0-9]+$" (lower $store.endpoint))) -}}
{{- fail "learning.store bearer token requires learning.store.tls.enabled=true for a non-loopback endpoint" -}}
{{- end -}}
{{- if and .Values.oidc.enabled $store.endpoint -}}
{{- fail "oidc.enabled cannot be combined with learning.store: ownership-enforced remote learning is unsupported" -}}
{{- end -}}
{{- range $env := .Values.extraEnv -}}
{{- if and (hasKey $env "name") (eq $env.name "MECATL_INSTALLATION_ID") -}}{{ fail "extraEnv name \"MECATL_INSTALLATION_ID\" collides with the installation identity environment variable owned by the chart" }}{{- end -}}
{{- if and (hasKey $env "name") (eq $env.name "MECATL_DRIVER_AUTH_TOKEN") -}}{{ fail "extraEnv name \"MECATL_DRIVER_AUTH_TOKEN\" collides with the learning store token environment variable owned by the chart" }}{{- end -}}
{{- end -}}
{{- end -}}
{{- define "mecak8s.validateBroker" -}}
{{- $b := .Values.broker -}}
{{- if and $b.image.digest $b.image.tag -}}{{ fail "set at most one of broker.image.digest or broker.image.tag" }}{{- end -}}
{{- if and $b.image.digest (not (regexMatch "^sha256:[0-9a-f]{64}$" $b.image.digest)) -}}{{ fail "broker.image.digest must be a lowercase sha256 digest" }}{{- end -}}
{{- $_ := required "broker.tls.secretName is required (including idle broker)" $b.tls.secretName -}}
{{- $_ := required "broker.clientCA.secretName is required" $b.clientCA.secretName -}}
{{- $_ := required "broker.clientCA.key is required" $b.clientCA.key -}}
{{- if eq $b.clientCA.key "token" }}{{ fail "broker.clientCA.key must not collide with the projected token path" }}{{- end -}}
{{- if eq $b.tls.certKey $b.tls.keyKey }}{{ fail "broker TLS certificate and key must use distinct Secret keys" }}{{- end -}}
{{- $_ := required "broker.workloadJWT.audience is required" $b.workloadJWT.audience -}}
{{- end -}}

{{/* Managed credential Redis: one headless Service is both the StatefulSet governing Service and the broker's address/SNI. */}}
{{- define "mecak8s.credentialRedisFullname" -}}
{{- printf "%s-credential-redis" (include "mecak8s.fullname" . | trunc 45 | trimSuffix "-") | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- define "mecak8s.credentialRedisLabels" -}}
app.kubernetes.io/name: mecabroker-credential-redis
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: credential-redis
{{- end -}}
{{- define "mecak8s.brokerOAuthCount" -}}
{{- $n := 0 -}}{{- range .Values.mcp.servers }}{{- if eq .auth.mode "oauth" }}{{- $n = add1 $n }}{{- end }}{{- end -}}{{ $n }}
{{- end -}}
{{- define "mecak8s.credentialRedisAddress" -}}
{{- $cs := .Values.broker.credentialStore -}}
{{- if $cs.managedRedis.enabled -}}{{ printf "%s.%s.svc:6379" (include "mecak8s.credentialRedisFullname" .) .Release.Namespace }}{{- else -}}{{ $cs.redis.address }}{{- end -}}
{{- end -}}

{{/* Validate broker credential custody: required with OAuth, forbidden without, external XOR managed. */}}
{{- define "mecak8s.validateBrokerCredentialStore" -}}
{{- $cs := .Values.broker.credentialStore -}}
{{- $oauth := gt (int (include "mecak8s.brokerOAuthCount" .)) 0 -}}
{{- $activated := or $cs.managedRedis.enabled (ne $cs.redis.address "") (ne $cs.redis.credentialsSecret "") (ne $cs.redis.caSecret "") (ne $cs.encryption.secretName "") (ne $cs.encryption.activeID "") (gt (len $cs.encryption.keys) 0) (ne $cs.managedRedis.tlsSecret "") (ne $cs.managedRedis.aclSecret "") -}}
{{- if and (not $oauth) $activated }}{{ fail "broker.credentialStore requires at least one OAuth MCP server" }}{{- end -}}
{{- if $oauth -}}
{{- if $cs.managedRedis.enabled -}}
{{- if or (ne $cs.redis.address "") (ne $cs.redis.caSecret "") }}{{ fail "broker.credentialStore.managedRedis.enabled derives the address and CA; leave redis.address and redis.caSecret empty" }}{{- end -}}
{{- $_ := required "broker.credentialStore.managedRedis.tlsSecret is required for managed Redis" $cs.managedRedis.tlsSecret -}}
{{- $_ := required "broker.credentialStore.managedRedis.aclSecret is required for managed Redis" $cs.managedRedis.aclSecret -}}
{{- if ne $cs.managedRedis.image.digest "sha256:bb186d083732f669da90be8b0f975a37812b15e913465bb14d845db72a4e3e08" }}{{ fail "broker.credentialStore.managedRedis.image.digest must be the verified redis:7.4.5-alpine digest" }}{{- end -}}
{{- else -}}
{{- $addr := required "broker.credentialStore.redis.address (or managedRedis.enabled) is required with OAuth MCP servers" $cs.redis.address -}}
{{- if or (regexMatch "[/@?#\\s]" $addr) (not (regexMatch "^(\\[[0-9A-Fa-f:.]+\\]|[A-Za-z0-9.-]+):[0-9]{1,5}$" $addr)) }}{{ fail "broker.credentialStore.redis.address must be a bare host:port" }}{{- end -}}
{{- end -}}
{{- $_ := required "broker.credentialStore.redis.credentialsSecret is required with OAuth MCP servers" $cs.redis.credentialsSecret -}}
{{- $_ := required "broker.credentialStore.encryption.secretName is required with OAuth MCP servers" $cs.encryption.secretName -}}
{{- $_ := required "broker.credentialStore.encryption.activeID is required with OAuth MCP servers" $cs.encryption.activeID -}}
{{- if eq (len $cs.encryption.keys) 0 }}{{ fail "broker.credentialStore.encryption.keys must list at least one KEK" }}{{- end -}}
{{- $seen := dict -}}{{- $active := 0 -}}
{{- range $cs.encryption.keys -}}
{{- if hasKey $seen .id }}{{ fail (printf "broker.credentialStore.encryption.keys id %q is duplicated" .id) }}{{- end -}}
{{- $_ := set $seen .id true -}}
{{- if eq .id $cs.encryption.activeID }}{{- $active = add1 $active }}{{- end -}}
{{- end -}}
{{- if ne $active 1 }}{{ fail "broker.credentialStore.encryption.activeID must match exactly one key" }}{{- end -}}
{{- $ms := dict -}}
{{- range $name, $value := dict "dialTimeout" $cs.redis.dialTimeout "operationTimeout" $cs.redis.operationTimeout "healthTimeout" $cs.redis.healthTimeout -}}
{{- $n := int64 (regexReplaceAll "(ms|s)$" ($value | toString) "") -}}
{{- $v := ternary $n (mul $n 1000) (hasSuffix "ms" ($value | toString)) -}}
{{- if or (le $v 0) (gt $v 30000) }}{{ fail (printf "broker.credentialStore.redis.%s must be positive and at most 30s" $name) }}{{- end -}}
{{- $_ := set $ms $name $v -}}
{{- end -}}
{{- if gt (get $ms "healthTimeout") (get $ms "operationTimeout") }}{{ fail "broker.credentialStore.redis.healthTimeout must not exceed operationTimeout" }}{{- end -}}
{{- end -}}
{{- end -}}

{{- define "mecak8s.brokerProtectedStorage" -}}
{{- $cs := .Values.broker.credentialStore -}}
{{- $redis := dict "address" (include "mecak8s.credentialRedisAddress" .) "password_file" "/var/run/mecabroker/credential-store/redis/password" "dial_timeout" $cs.redis.dialTimeout "operation_timeout" $cs.redis.operationTimeout "health_timeout" $cs.redis.healthTimeout -}}
{{- if $cs.redis.usernameKey }}{{- $_ := set $redis "username_file" "/var/run/mecabroker/credential-store/redis/username" }}{{- end -}}
{{- if or $cs.managedRedis.enabled $cs.redis.caSecret }}{{- $_ := set $redis "ca_file" "/var/run/mecabroker/credential-store/redis/ca.pem" }}{{- end -}}
{{- $keys := list -}}{{- range $cs.encryption.keys }}{{- $keys = append $keys (dict "id" .id "file" (printf "/var/run/mecabroker/credential-store/encryption/%s" .id)) }}{{- end -}}
{{- dict "redis" $redis "encryption" (dict "active_id" $cs.encryption.activeID "keys" $keys) | toJson -}}
{{- end -}}

{{- define "mecak8s.brokerConfig" -}}
{{- $profiles := list -}}
{{- range $index, $server := .Values.mcp.servers -}}
{{- if eq $server.auth.mode "oauth" -}}
{{- if or (gt (len $server.auth.oauth.network.additionalOrigins) 0) (gt (len $server.auth.oauth.network.privateOrigins) 0) (ne (int $server.auth.oauth.network.maxRedirects) 0) }}{{ fail (printf "mcp.servers[%d].auth.oauth.network is unsupported by mecabroker" $index) }}{{- end -}}
{{- if and (eq $server.auth.oauth.client.mode "dcr") (or (not $server.auth.oauth.upstream) (ne $server.auth.oauth.upstream.mode "oauth2")) }}{{ fail (printf "mcp.servers[%d] DCR requires explicit OAuth2 authorizationEndpoint and tokenEndpoint" $index) }}{{- end -}}
{{- if eq $server.auth.oauth.client.mode "preregistered" -}}
{{- $clientID := $server.auth.oauth.client.preregistered.id -}}
{{- if or (eq (trim $clientID) "") (regexMatch "[\x00-\x1f\x7f]" $clientID) }}{{ fail "OAuth client id must be non-blank and contain no control characters" }}{{- end -}}
{{- end -}}
{{- $oauth := dict "scopes" $server.auth.oauth.scopes "request_refresh_token" (default false $server.auth.oauth.requestRefreshToken) -}}
{{- if eq $server.auth.oauth.client.mode "preregistered" }}{{- $_ := set $oauth "client_mode" "preregistered" }}{{- $_ := set $oauth "client_id" $server.auth.oauth.client.preregistered.id }}{{- $_ := set $oauth "client_secret_file" (printf "/var/run/mecabroker/mcp-oauth/%d/client-secret" $index) }}{{- else if eq $server.auth.oauth.client.mode "cimd" }}{{- $_ := set $oauth "client_mode" "cimd" }}{{- $_ := set $oauth "cimd_document_url" $server.auth.oauth.client.cimd.documentURL }}{{- else }}{{- $_ := set $oauth "client_mode" "dcr" }}{{- $_ := set $oauth "dcr_discovery_url" $server.auth.oauth.client.dcr.discoveryURL }}{{- end -}}
{{- if $server.auth.oauth.issuer }}{{- $_ := set $oauth "issuer" $server.auth.oauth.issuer }}{{- end -}}
{{- if and $server.auth.oauth.upstream (eq $server.auth.oauth.upstream.mode "oauth2") }}{{- $_ := set $oauth "authorization_endpoint" $server.auth.oauth.upstream.oauth2.authorizationEndpoint }}{{- $_ := set $oauth "token_endpoint" $server.auth.oauth.upstream.oauth2.tokenEndpoint }}{{- end -}}
{{- $profile := dict "name" $server.name "url" $server.url "auth" "oauth" "oauth" $oauth -}}
{{- if $server.auth.oauth.tools }}{{- $tools := list }}{{- range $server.auth.oauth.tools }}{{- $tools = append $tools (dict "name" .name "description" .description "schema" .inputSchema "read_only" (default false .readOnly)) }}{{- end }}{{- $_ := set $profile "tools" $tools }}{{- end -}}
{{- $profiles = append $profiles $profile -}}
{{- else if and $.Values.mcp.broker.callbackURL (eq $server.auth.mode "none") -}}
{{- $profiles = append $profiles (dict "name" $server.name "url" $server.url "auth" "none") -}}
{{- end -}}
{{- end -}}
{{- $workload := dict "audience" .Values.broker.workloadJWT.audience "subject" (printf "system:serviceaccount:%s:%s" .Release.Namespace (include "mecak8s.fullname" .)) "trust_bundle_file" "/var/run/mecabroker/workload-jwt/ca.pem" "max_jwks_staleness" "900s" "kubernetes_bootstrap" (dict "discovery_url" "https://kubernetes.default.svc/.well-known/openid-configuration" "jwks_uri" "https://kubernetes.default.svc/openid/v1/jwks" "token_file" "/var/run/mecabroker/workload-jwt/token") -}}
{{- $config := dict "api_version" "mecabroker.mecatl.dev/v1" "listener" (dict "public_address" "0.0.0.0:8443" "tls_cert_file" (printf "/var/run/mecabroker/tls/%s" .Values.broker.tls.certKey) "tls_key_file" (printf "/var/run/mecabroker/tls/%s" .Values.broker.tls.keyKey)) "workload_jwt" $workload "callback_url" .Values.mcp.broker.callbackURL "profiles" $profiles "drain" (dict "propagation_delay" "2s" "timeout" "55s" "listener_shutdown_timeout" "5s") "transport" (dict "rpc_deadline" "10s" "execute_deadline" "120s" "handle_idle_timeout" "300s" "sweep_interval" "30s" "cleanup_timeout" "10s" "max_handles" 128 "max_owners" 128 "max_receipts" 4096 "max_receipt_bytes" 8388608 "max_pending_controls" 1024 "max_active_executes" 64) "runtime" (dict "max_logical_sessions" 1024 "logical_retention" "86400s" "max_pending_auth_states" 1024) -}}
{{- if gt (int (include "mecak8s.brokerOAuthCount" .)) 0 }}{{- $_ := set $config "protected_storage" (include "mecak8s.brokerProtectedStorage" . | fromJson) }}{{- end -}}
{{- $config | toPrettyJson -}}
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

{{/* Validate canonical MCP input before routing OAuth entries through the singleton. */}}
{{- define "mecak8s.validateMCP" -}}
{{- $seen := dict -}}
{{- $ownedEnv := dict -}}
{{- $oauthCount := 0 -}}
{{- $staticCount := 0 -}}
{{- range $server := .Values.mcp.servers -}}
{{- $folded := lower $server.name -}}
{{- if hasKey $seen $folded }}{{ fail (printf "mcp.servers name %q is duplicated case-insensitively" $server.name) }}{{- end -}}
{{- $_ := set $seen $folded true -}}
{{- if eq $server.auth.mode "staticBearer" }}{{- $_ := set $ownedEnv (printf "MCP_%s_TOKEN" (upper $server.name)) true }}{{- $staticCount = add1 $staticCount }}{{- end -}}
{{- if eq $server.auth.mode "oauth" }}{{- $oauthCount = add1 $oauthCount }}{{- if $server.insecureHTTP }}{{ fail (printf "mcp.servers[%s].insecureHTTP is invalid for oauth" $server.name) }}{{- end }}{{- range $scope := $server.auth.oauth.scopes }}{{- if or (eq (trim $scope) "") (regexMatch "[\x00-\x1f\x7f]" $scope) }}{{ fail (printf "mcp.servers[%s].auth.oauth.scopes must be non-blank and contain no control characters" $server.name) }}{{- end }}{{- end }}{{- end -}}
{{- end -}}
{{- if and (gt $oauthCount 0) (gt $staticCount 0) }}{{ fail "mcp.servers staticBearer is unsupported with broker OAuth" }}{{- end -}}
{{- if and (gt $oauthCount 0) (not .Values.oidc.enabled) }}{{ fail "mcp OAuth requires oidc.enabled=true for verified broker-control caller identity" }}{{- end -}}
{{- if and (gt $oauthCount 0) (eq (trim .Values.mcp.broker.callbackURL) "") }}{{ fail "mcp.broker.callbackURL is required with an OAuth MCP server" }}{{- end -}}
{{- if and (ne (trim .Values.mcp.broker.callbackURL) "") (eq $oauthCount 0) }}{{ fail "mcp.broker.callbackURL requires at least one OAuth MCP server" }}{{- end -}}
{{- range $env := .Values.extraEnv }}{{- if and (hasKey $env "name") (hasKey $ownedEnv $env.name) }}{{ fail (printf "extraEnv name %q collides with an MCP authentication environment variable owned by the chart" $env.name) }}{{- end }}{{- end -}}
{{- end -}}

{{/* Agent only selects remote authority; all routes and OAuth material belong to the broker. */}}
{{- define "mecak8s.mcpOAuthSettings" -}}
mcp:
  mode: {{ if .Values.mcp.broker.callbackURL }}broker{{ else }}global{{ end }}
{{- end -}}
