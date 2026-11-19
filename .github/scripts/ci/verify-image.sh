#!/usr/bin/env bash
set -euo pipefail

image=${1:?Image tag required}
tmp=$(mktemp -d)
container=
cleanup() {
    if [[ -n "$container" ]]; then
        docker rm -v "$container" >/dev/null
    fi
    rm -f "$tmp/caddy" "$tmp/compose.yaml" "$tmp/.env"
    rmdir "$tmp"
}
trap cleanup EXIT

cp compose.yaml "$tmp/compose.yaml"
cat > "$tmp/.env" <<'EOF'
DBS_SOAP_USERNAME=example
DBS_SOAP_PASSWORD=example
DBS_SOAP_ENDPOINT=https://example.test/soap.php
MAIL_API_URL=https://mail.example.test
MAIL_AUTH_KEY=fixture-auth
MAIL_ADAPTER_KEY=fixture-adapter
MAIL_TO_TARGET=ops@example.test
EOF
docker compose --env-file "$tmp/.env" -f "$tmp/compose.yaml" config --quiet

docker run --rm --network none "$image" caddy list-modules --skip-standard |
    grep -Fx dns.providers.domain_bestellsystem

docker run --rm --network none \
    --env-file "$tmp/.env" \
    "$image" caddy validate --config /etc/caddy/Caddyfile --adapter caddyfile

docker run --rm --network none \
    --env-file "$tmp/.env" \
    -v "$PWD/sites:/etc/caddy/sites:ro" \
    "$image" caddy validate --config /etc/caddy/Caddyfile --adapter caddyfile

for ca in https://acme-v02.api.letsencrypt.org/directory https://acme-staging-v02.api.letsencrypt.org/directory; do
    ca_env=()
    if [[ "$ca" == https://acme-staging-v02.api.letsencrypt.org/directory ]]; then
        ca_env=(-e "CADDY_ACME_CA=$ca")
    fi
    docker run --rm --network none "${ca_env[@]}" \
        -v "$PWD/sites:/etc/caddy/sites:ro" \
        "$image" caddy adapt --config /etc/caddy/Caddyfile --adapter caddyfile |
        jq -e --arg ca "$ca" 'any(.. | objects; .ca? == $ca)' >/dev/null
done

container=$(docker create "$image")
docker cp "$container:/usr/bin/caddy" "$tmp/caddy"
go run golang.org/x/vuln/cmd/govulncheck@v1.10.0 -mode binary "$tmp/caddy"
