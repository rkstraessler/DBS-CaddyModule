# Caddy DNS provider for Domain-Bestellsystem

[![CI](https://github.com/rkstraessler/DBS-CaddyModule/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/rkstraessler/DBS-CaddyModule/actions/workflows/ci.yml)

Caddy DNS provider for Domain-Bestellsystem. Automate ACME DNS-01 challenges and wildcard certificates. Caddy discovers the public authoritative zone and waits for the challenge record to propagate. The `dns.providers.domain_bestellsystem` module manages TXT records over SOAP 1.1.

[GitHub Releases](https://github.com/rkstraessler/DBS-CaddyModule/releases) · [Report an issue](https://github.com/rkstraessler/DBS-CaddyModule/issues/new)

[Caddy TLS](https://caddyserver.com/docs/caddyfile/directives/tls) · [Domain-Bestellsystem WSDL](https://soap.domain-bestellsystem.de/soap.php?wsdl)

## Installation

Requires a Linux amd64 host with Docker Compose v2 and a Domain-Bestellsystem SOAP account that can manage your public DNS zone. The quick start uses the published GHCR image.

## Quick start

Run this from the directory where you want to create the stack.

```sh
base=https://raw.githubusercontent.com/rkstraessler/DBS-CaddyModule/v1.0.0
mkdir dbs-caddy &&
    cd dbs-caddy &&
    mkdir sites &&
    curl -fsSLo compose.yaml "$base/compose.yaml" &&
    curl -fsSLo Caddyfile "$base/Caddyfile" &&
    curl -fsSLo sites/api.caddy "$base/sites/api.caddy" &&
    curl -fsSLo .env.example "$base/.env.example" &&
    cp .env.example .env
```

### Configuration

Fill in `.env` before starting Caddy. For partner accounts, use the full `PARTNER-ID/SOAP-USER` login. Use the HTTPS endpoint assigned to your account and wrap real values containing `$` or `#` in single quotes. Set `CADDY_IMAGE` to a version or digest for repeatable upgrades and rollback.

```dotenv
DBS_SOAP_USERNAME=
DBS_SOAP_PASSWORD=
DBS_SOAP_ENDPOINT=https://soap.domain-bestellsystem.de/soap.php
CADDY_IMAGE=ghcr.io/rkstraessler/caddy-domainbestellsystem:1.0.0
```

In `sites/api.caddy`, replace `api.example.com` and `anonymizer:3000` with your public hostname and upstream address. Attach any upstream containers to the shared `proxy` network and make TCP 443 reachable by clients.

```sh
docker network inspect proxy >/dev/null 2>&1 || docker network create proxy
docker compose up -d
docker compose ps
```

Caddy stores certificates and ACME account data in `caddy_data`. Preserve that volume when updating the stack. [Compose file](compose.yaml) · [Environment example](.env.example)

### Komodo

Deploy the same `compose.yaml` as a stack, keep `Caddyfile` and `sites/` beside it, and enter the `.env` values in the stack environment.

### Site configuration

Add one file per site under `sites/` with a `.caddy` extension.

```caddyfile
api.example.com {
    tls {
        dns domain_bestellsystem
    }

    reverse_proxy anonymizer:3000
}
```

Public DNS must serve the challenge TXT record from the authoritative zone. An internal DNS view can point the application hostname at a private Caddy address while Let's Encrypt resolves the challenge through public DNS. When split DNS interferes with zone discovery, set `resolvers` in the `tls` block to recursive resolvers that can see public DNS. [Caddy DNS challenge options](https://caddyserver.com/docs/caddyfile/directives/tls#syntax)

## Troubleshooting

- **HTTP 401 or SOAP authentication error** — Verify the full Basic Auth login, SOAP account permissions, and endpoint in Komodo. Errors report status or return codes with credentials and server-supplied message text omitted.
- **SOAP code 2303 during zone lookup** — Check which public zone is authoritative and whether the SOAP account manages it. For CNAME delegation, configure `dns_challenge_override_domain` with the complete challenge target name in a zone the account can modify.
- **Propagation timeout** — Inspect the TXT record through public DNS and at authoritative nameservers. Adjust Caddy's `propagation_timeout` if needed. Reads retry bounded transient failures. Writes execute once per call because the vendor contract does not provide a write idempotency key. If a response is lost, inspect the zone before removing any stale challenge record.
- **Parallel challenges** — Individual vendor operations preserve other records, including different values at the same name. In-process operations for a zone are serialized. Separate Caddy processes need coordinated certificate storage if they manage the same certificates. The vendor exposes neither record IDs nor a compare-and-delete operation for independently recreating an identical record.

## Development

Requires Go 1.27.4 and Docker with Compose v2. The Dockerfile uses the official Caddy 2.11.9 Alpine builder and runtime images, pinned by digest. The module uses libdns 1.1.1. The image includes dependency license notices under `/usr/share/licenses/caddy-domainbestellsystem/`.

```sh
go test -race ./...
docker build -t dbs-caddy:local .
docker run --rm --network none dbs-caddy:local caddy list-modules --skip-standard
```

The tests use a local TLS SOAP mock based on the vendor WSDL and DNS examples. CI verifies the Go source, race behavior, dependencies, the Caddy image and Caddyfile, and scans the built executable with govulncheck. For a separately authorized live test, configure a disposable public domain and a separate Caddy data volume, then set the global `acme_ca` to `https://acme-staging-v02.api.letsencrypt.org/directory`. Confirm that issuance succeeds, both ordinary and wildcard challenges work, and the TXT records are removed. Switch back to the production directory and data volume for deployment. [Let's Encrypt staging](https://letsencrypt.org/docs/staging-environment/)

### GitHub automation

- **Branch protection** requires an up-to-date pull request, one current approval, resolved conversations, and the GitHub Actions `Required CI` check. GitHub uses squash merges and deletes merged branches. Repository administrators have a bypass for review and CI. A separate rule blocks force pushes and deletion for everyone. [Review and CI rules](.github/main-ruleset.json) · [History rules](.github/main-history-ruleset.json)
- **Dependabot** checks Go modules, GitHub Actions, and the Caddy base images daily at 03:00 `Europe/Berlin`. It groups compatible Go and Actions updates and keeps the Caddy base images together. [Dependabot configuration](.github/dependabot.yml)
- **PR maintenance** updates conflict-free branches in this repository for pull requests targeting `main`. The `reikem-github-pr-maintenance` app updates branches so CI runs on the new head. The Dependabot workflow checks the current signed revision, approves eligible patch and minor updates after `Required CI`, and requests a squash merge. Major and unclassified updates need manual review. [Branch updater](.github/workflows/update-pr-branches.yml) · [Dependabot merge workflow](.github/workflows/dependabot.yml)
- **autofix.ci** runs gofmt on pull requests. Its commits rerun CI. [Autofix workflow](.github/workflows/autofix.yml)

Install the official [autofix.ci app](https://github.com/apps/autofix-ci) and the repository-scoped PR maintenance app. Set the repository variable `PR_AUTOMATION_SLUG`. In the `pr-branch-updates` environment, restrict deployments to `main`, set `PR_AUTOMATION_CLIENT_ID`, and add the secret `PR_AUTOMATION_PRIVATE_KEY`. Keep default workflow permissions read-only and allow GitHub Actions to approve pull requests for the Dependabot workflow. Enable Dependabot alerts, security updates, and private vulnerability reporting. To synchronize labels, run `bash .github/scripts/sync-labels.sh`. [Security policy](SECURITY.md)

## Releases

1. Create an annotated SemVer tag on a verified commit on `main`. The active `v*` rulesets restrict tag creation to administrators and block tag updates and deletions.
2. Wait for the release workflow to verify the tagged source, publish the checked image to GHCR, and generate the GitHub release notes.
3. Verify an unauthenticated pull from GHCR. Stable releases update the major, minor, and `latest` aliases when the release is newer for that channel. Prereleases publish only their full version tag.

[Release workflow](.github/workflows/release.yml) · [GitHub package visibility](https://docs.github.com/en/packages/learn-github-packages/configuring-a-packages-access-control-and-visibility)

## License

The provider source is MIT-licensed, copyright REIKEM. Caddy and libdns retain their upstream licenses. [MIT license](LICENSE) · [Caddy license](https://github.com/caddyserver/caddy/blob/v2.11.9/LICENSE) · [libdns license](https://github.com/libdns/libdns/blob/v1.1.1/LICENSE)
