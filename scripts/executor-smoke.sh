#!/bin/sh
# Smoke-tests the executor workload image: builds it, starts mecatl-executor
# serve with throwaway mTLS certificates, and checks it through mecaexec.
# With --keep, leaves the container and certificates for manual testing;
# EXECUTOR_SMOKE_PORT pins the host port so it survives a docker restart.
set -eu

keep=false
[ "${1:-}" = --keep ] && keep=true

cd "$(git rev-parse --show-toplevel)"
work=$(mktemp -d "${TMPDIR:-/tmp}/executor-smoke.XXXXXX")
name=executor-smoke-$$
image=mecatl-executor:smoke
client_uri=spiffe://mecatl.test/provider
failed=0
passed=false

cleanup() {
	if [ "$passed" = false ]; then
		echo "--- executor logs"
		docker logs "$name" 2>&1 | tail -50 || true
	fi
	if [ "$keep" = false ] || [ "$passed" = false ]; then
		docker rm -f "$name" >/dev/null 2>&1 || true
		rm -rf "$work"
	fi
}
trap cleanup EXIT
trap 'exit 130' INT TERM

issue() { # out-prefix CN SAN EKU
	openssl req -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -subj "/CN=$2" \
		-keyout "$1.key" -out "$1.csr" 2>/dev/null
	printf 'subjectAltName=%s\nextendedKeyUsage=%s\n' "$3" "$4" >"$1.ext"
	openssl x509 -req -in "$1.csr" -CA "$work/ca.crt" -CAkey "$work/ca.key" \
		-days 1 -extfile "$1.ext" -out "$1.crt" 2>/dev/null
	rm "$1.csr" "$1.ext"
}

mkdir -p "$work/tls" "$work/provider" "$work/client"
openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -days 1 \
	-subj /CN=executor-smoke-ca -keyout "$work/ca.key" -out "$work/ca.crt" 2>/dev/null
issue "$work/tls/tls" executor "DNS:localhost,IP:127.0.0.1" serverAuth
issue "$work/client/client" provider "URI:$client_uri" clientAuth
cp "$work/ca.crt" "$work/provider/ca.crt"
printf '%s\n' "$client_uri" >"$work/provider/client-uri"
# The image runs as 65532 and must read the mounted key.
chmod 644 "$work/tls/tls.key"

go build -o "$work/mecaexec" ./cmd/mecaexec
docker build -q -t "$image" -f build/execution-workload/Dockerfile . >/dev/null
docker run -d --name "$name" -p "127.0.0.1:${EXECUTOR_SMOKE_PORT:-}:7443" \
	-v "$work/tls:/etc/mecatl-executor/tls:ro" \
	-v "$work/provider:/etc/mecatl-executor/provider:ro" \
	"$image" -listen 0.0.0.0:7443 -files -commands >/dev/null
addr=$(docker port "$name" 7443/tcp | head -1)

mx() {
	"$work/mecaexec" -addr "$addr" -ca "$work/ca.crt" \
		-cert "$work/client/client.crt" -key "$work/client/client.key" "$@"
}

ready=false
for _ in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20; do
	if mx -timeout 2s ls >/dev/null 2>&1; then
		ready=true
		break
	fi
	sleep 0.5
done
if [ "$ready" = false ]; then
	echo "FAIL executor did not become ready"
	exit 1
fi

check() { # name want got
	if [ "$2" = "$3" ]; then
		echo "ok   $1"
	else
		printf 'FAIL %s\n  want: %s\n  got:  %s\n' "$1" "$2" "$3"
		failed=1
	fi
}

echo smoke | mx create smoke.txt || true
check "file round trip" smoke "$(mx read smoke.txt 2>&1)"
check "commands run as the image user" 65532 "$(mx run 'id -u' 2>/dev/null)"
rc=0
out=$(mx run 'echo out; exit 3' 2>/dev/null) || rc=$?
check "command output" out "$out"
check "command exit status" 3 "$rc"
refused=$("$work/mecaexec" -addr "$addr" -ca "$work/ca.crt" \
	-cert "$work/tls/tls.crt" -key "$work/tls/tls.key" ls 2>&1 || true)
case "$refused" in
*"bad certificate"*) check "non-provider certificate refused" refused refused ;;
*) check "non-provider certificate refused" refused "$refused" ;;
esac

if [ "$failed" -ne 0 ]; then
	exit 1
fi
passed=true
if [ "$keep" = true ]; then
	echo "container $name kept; certificates in $work"
	echo "mecaexec: $work/mecaexec -addr $addr -ca $work/ca.crt" \
		"-cert $work/client/client.crt -key $work/client/client.key"
	echo "cleanup: docker rm -f $name && rm -rf $work"
fi
