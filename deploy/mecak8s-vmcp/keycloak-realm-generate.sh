#!/usr/bin/env bash
# Regenerates deploy/mecak8s-vmcp/keycloak.yaml's realm JSON.
#
# WHY THIS EXISTS: a realm import whose `clientScopes` array is hand-written REPLACES
# Keycloak's built-in client scopes instead of adding to them. Losing `basic` drops the
# `sub` claim from access tokens; losing `profile`/`email` drops the identity claims.
# So we let Keycloak seed a realm normally, add only what we need on top, and export the
# result. The EXPORT is the declarative artifact; this script is just how it was made.
#
# Run against a running fixture Keycloak. Output: realm.json on stdout.
set -euo pipefail

CA=${CA:-.scratch/kind/mecatl-dev/fixture-ca.crt}
KCROOT=${KCROOT:-https://keycloak.mecatl-vmcp.svc.cluster.local:8443}
RESOLVE=(--cacert "$CA" --resolve keycloak.mecatl-vmcp.svc.cluster.local:8443:127.0.0.1)
REALM=mecatl
VMCP_AUDIENCE=${VMCP_AUDIENCE:-http://127.0.0.1:18080/mcp}

adm() { curl -s "${RESOLVE[@]}" -H "Authorization: Bearer $TOKEN" "$@"; }

TOKEN=$(curl -s "${RESOLVE[@]}" -X POST "$KCROOT/realms/master/protocol/openid-connect/token" \
  -d grant_type=password -d client_id=admin-cli -d username=admin -d password=admin | jq -r .access_token)
[ -n "$TOKEN" ] && [ "$TOKEN" != null ] || { echo "admin auth failed" >&2; exit 1; }

# Recreate the realm WITHOUT a clientScopes override so the built-ins are seeded.
adm -X DELETE "$KCROOT/admin/realms/$REALM" >/dev/null || true
adm -X POST "$KCROOT/admin/realms" -H 'Content-Type: application/json' -d @- >/dev/null <<JSON
{"realm":"$REALM","enabled":true,"sslRequired":"external","accessTokenLifespan":900,"ssoSessionIdleTimeout":1800}
JSON

# The one custom scope: an application-semantic scope that also stamps the shared
# platform audience into `aud`.
#
# The audience MUST equal incomingAuth.resourceUrl. The operator derives the auth
# server's allowed_audiences from resourceUrl alone (deriveAllowedAudiences), and a
# delegate client's audiences must be a subset of that -- so an opaque identifier is
# not reachable here even though the token-exchange `audience` parameter accepts one.
# resourceUrl is the RFC 9728 protected-resource metadata URL, so it must stay a URL.
adm -X POST "$KCROOT/admin/realms/$REALM/client-scopes" -H 'Content-Type: application/json' -d @- >/dev/null <<JSON
{"name":"mcp:read","description":"Read access to vMCP-aggregated tools","protocol":"openid-connect",
 "attributes":{"include.in.token.scope":"true","display.on.consent.screen":"false"},
 "protocolMappers":[{"name":"vmcp-audience","protocol":"openid-connect","protocolMapper":"oidc-audience-mapper",
   "config":{"included.custom.audience":"$VMCP_AUDIENCE","access.token.claim":"true","id.token.claim":"false"}}]}
JSON
SCOPE_ID=$(adm "$KCROOT/admin/realms/$REALM/client-scopes" | jq -r '.[]|select(.name=="mcp:read")|.id')

for c in vmcp-browser mecatui-kind; do
  case $c in
    vmcp-browser) REDIRECTS='["http://127.0.0.1:18080/oauth/callback"]'; DIRECT=false ;;
    mecatui-kind) REDIRECTS='["http://127.0.0.1:18473/oauth/callback","http://127.0.0.1:19999/probe"]'; DIRECT=true ;;
  esac
  adm -X POST "$KCROOT/admin/realms/$REALM/clients" -H 'Content-Type: application/json' -d @- >/dev/null <<JSON
{"clientId":"$c","enabled":true,"publicClient":true,"standardFlowEnabled":true,
 "directAccessGrantsEnabled":$DIRECT,"redirectUris":$REDIRECTS,"webOrigins":[],
 "attributes":{"pkce.code.challenge.method":"S256"}}
JSON
done

# mcp:read is DEFAULT on the caller client (so its tokens carry the scope and audience
# without asking) and OPTIONAL on the browser client (which only establishes identity).
CID=$(adm "$KCROOT/admin/realms/$REALM/clients" | jq -r '.[]|select(.clientId=="mecatui-kind")|.id')
adm -X PUT "$KCROOT/admin/realms/$REALM/clients/$CID/default-client-scopes/$SCOPE_ID" >/dev/null
BID=$(adm "$KCROOT/admin/realms/$REALM/clients" | jq -r '.[]|select(.clientId=="vmcp-browser")|.id')
adm -X PUT "$KCROOT/admin/realms/$REALM/clients/$BID/optional-client-scopes/$SCOPE_ID" >/dev/null

# The raw export is ~2700 lines, almost all of it Keycloak's own defaults:
# authenticationFlows, roles, components, requiredActions, the six built-in clients,
# and 12 client scopes the fixture never uses. Keycloak recreates every one of those
# for a fresh realm, so committing them is review noise that hides the ~50 lines that
# are actually a decision. We keep ONLY:
#
#   clientScopes : basic (supplies `sub`), profile, email, and our mcp:read
#   clients      : the two fixture clients
#   users        : re-attached below, since partial-export omits them
#
# The built-ins are KEPT RATHER THAN OMITTED because a realm import that declares a
# `clientScopes` array REPLACES Keycloak's built-ins instead of merging -- dropping
# `basic` silently removes `sub` from every access token. They are copied verbatim
# from the export rather than hand-written, for the same reason.
#
# Note: partial-export is a POST endpoint; a GET returns 404.
KEEP_SCOPES='["basic","profile","email","mcp:read"]'
KEEP_CLIENTS='["vmcp-browser","mecatui-kind"]'
adm -X POST "$KCROOT/admin/realms/$REALM/partial-export?exportClients=true&exportGroupsAndRoles=true" \
| jq --argjson keepScopes "$KEEP_SCOPES" --argjson keepClients "$KEEP_CLIENTS" '
    {realm, enabled, sslRequired, accessTokenLifespan, ssoSessionIdleTimeout}
    + {clientScopes: [.clientScopes[] | select(.name as $n | $keepScopes | index($n))]}
    + {clients: [.clients[]
        | select(.clientId as $c | $keepClients | index($c))
        # Prune scope references to the ones that survive the filter above; a client
        # pointing at a clientScope the realm no longer defines imports as broken.
        | .defaultClientScopes  = [(.defaultClientScopes  // [])[] | select(. as $n | $keepScopes | index($n))]
        | .optionalClientScopes = [(.optionalClientScopes // [])[] | select(. as $n | $keepScopes | index($n))]
      ]}
  ' \
| jq --argjson users '[
  {"username":"alice","enabled":true,"emailVerified":true,"email":"alice@example.com",
   "firstName":"Alice","lastName":"Example",
   "credentials":[{"type":"password","value":"Secret123","temporary":false}]},
  {"username":"bob","enabled":true,"emailVerified":true,"email":"bob@example.com",
   "firstName":"Bob","lastName":"Example",
   "credentials":[{"type":"password","value":"Secret123","temporary":false}]}
]' '. + {users: $users}'
