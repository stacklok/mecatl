#!/bin/sh
# broker-client-key-set.sh reads the identity broker's client key, a PEM EC
# P-256 private key, on stdin and writes its public JWK Set (RFC 7517) to
# stdout: one ES256 signing key whose kid is its RFC 7638 thumbprint, as the
# broker checks at startup. Only the public key leaves the first openssl.
#
#   openssl ecparam -name prime256v1 -genkey -noout > key.pem
#   deploy/mecak8s-idbroker/broker-client-key-set.sh < key.pem > jwks.json

set -eu

pub=$(openssl ec -pubout 2>/dev/null) || {
	echo 'stdin is not a PEM EC private key' >&2
	exit 1
}
der() { printf '%s\n' "$pub" | openssl ec -pubin -outform DER 2>/dev/null; }
b64url() { openssl base64 -A | tr '+/' '-_' | tr -d '='; }

# A P-256 public key's DER is 91 bytes and ends with the point: x, then y.
[ "$(der | wc -c | tr -d ' ')" -eq 91 ] || {
	echo 'the key is not EC P-256' >&2
	exit 1
}
x=$(der | tail -c 64 | head -c 32 | b64url)
y=$(der | tail -c 32 | b64url)
# RFC 7638: the SHA-256 of the required members in lexicographic order.
kid=$(printf '{"crv":"P-256","kty":"EC","x":"%s","y":"%s"}' "$x" "$y" | openssl dgst -sha256 -binary | b64url)
printf '{"keys":[{"kty":"EC","crv":"P-256","x":"%s","y":"%s","kid":"%s","use":"sig","alg":"ES256"}]}\n' "$x" "$y" "$kid"
