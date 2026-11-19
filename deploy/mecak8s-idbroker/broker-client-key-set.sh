#!/bin/sh
# broker-client-key-set.sh reads the identity broker's client key, a PEM EC
# P-256 private key, on stdin and writes its public JWK Set (RFC 7517) to
# stdout: one ES256 signing key whose kid is its RFC 7638 thumbprint, as the
# broker checks at startup. Only the public key leaves the first openssl.
#
#   openssl ecparam -name prime256v1 -genkey -noout > key.pem
#   deploy/mecak8s-idbroker/broker-client-key-set.sh < key.pem > jwks.json
#
# POSIX sh has no pipefail, so each openssl step is checked on its own, and
# the values are checked before anything is written.

set -eu

fail() {
	echo "$1" >&2
	exit 1
}

der=$(mktemp)
trap 'rm -f "$der"' EXIT

pub=$(openssl ec -pubout 2>/dev/null) || fail 'stdin is not a PEM EC private key'
printf '%s\n' "$pub" | openssl ec -pubin -outform DER >"$der" 2>/dev/null || fail 'openssl could not encode the public key'

# A P-256 public key's DER is 91 bytes and ends with the point: x, then y.
[ "$(wc -c <"$der" | tr -d ' ')" -eq 91 ] || fail 'the key is not EC P-256'

b64url() { openssl base64 -A | tr '+/' '-_' | tr -d '='; }
x=$(tail -c 64 "$der" | dd bs=1 count=32 2>/dev/null | b64url)
y=$(tail -c 32 "$der" | b64url)
# RFC 7638: the SHA-256 of the required members in lexicographic order.
kid=$(printf '{"crv":"P-256","kty":"EC","x":"%s","y":"%s"}' "$x" "$y" | openssl dgst -sha256 -binary | b64url)

# 32 bytes in unpadded base64url are 43 characters; a failed step in a
# pipeline above leaves a value short or empty.
for value in "$x" "$y" "$kid"; do
	[ "${#value}" -eq 43 ] || fail 'openssl produced a malformed coordinate or thumbprint'
done
printf '{"keys":[{"kty":"EC","crv":"P-256","x":"%s","y":"%s","kid":"%s","use":"sig","alg":"ES256"}]}\n' "$x" "$y" "$kid"
