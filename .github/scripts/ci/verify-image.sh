#!/usr/bin/env bash
set -euo pipefail

image=${1:?Image tag required}
docker run --rm --network none "$image" caddy list-modules --skip-standard |
    grep -Fx dns.providers.domain_bestellsystem

docker run --rm --network none \
    -e DBS_SOAP_USERNAME=example -e DBS_SOAP_PASSWORD=example \
    -e DBS_SOAP_ENDPOINT=https://example.test/soap.php \
    -v "$PWD/Caddyfile:/etc/caddy/Caddyfile:ro" \
    -v "$PWD/sites:/etc/caddy/sites:ro" \
    "$image" caddy validate --config /etc/caddy/Caddyfile --adapter caddyfile

DBS_SOAP_USERNAME=example DBS_SOAP_PASSWORD=example \
    DBS_SOAP_ENDPOINT=https://example.test/soap.php docker compose config --quiet

tmp=$(mktemp -d)
container=
cleanup() {
    if [[ -n "$container" ]]; then
        docker rm -v "$container" >/dev/null
    fi
    rm -f "$tmp/caddy"
    rmdir "$tmp"
}
trap cleanup EXIT
container=$(docker create "$image")
docker cp "$container:/usr/bin/caddy" "$tmp/caddy"
go run golang.org/x/vuln/cmd/govulncheck@v1.10.0 -mode binary "$tmp/caddy"
