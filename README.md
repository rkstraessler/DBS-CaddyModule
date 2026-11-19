# Caddy DNS provider for Domain-Bestellsystem

Caddy module `dns.providers.domain_bestellsystem` enables Let's Encrypt DNS-01 for domains hosted on Domain-Bestellsystem, including wildcard certificates. Caddy discovers the public authoritative DNS zone and waits for the TXT record to propagate. The module manages individual TXT records over SOAP 1.1. [Caddy TLS](https://caddyserver.com/docs/caddyfile/directives/tls) · [Domain-Bestellsystem WSDL](https://soap.domain-bestellsystem.de/soap.php?wsdl)

## Quick start with Docker Compose

Use a Linux amd64 host with Docker Compose v2 and a Domain-Bestellsystem SOAP account that can manage your public DNS zone. Save the following files in your stack directory.

### `compose.yaml`

```yaml
services:
    caddy:
        image: ${CADDY_IMAGE:-ghcr.io/rkstraessler/caddy-domainbestellsystem:latest}
        restart: unless-stopped
        ports:
            - "443:443"
        environment:
            DBS_SOAP_USERNAME: ${DBS_SOAP_USERNAME:?Set DBS_SOAP_USERNAME}
            DBS_SOAP_PASSWORD: ${DBS_SOAP_PASSWORD:?Set DBS_SOAP_PASSWORD}
            DBS_SOAP_ENDPOINT: ${DBS_SOAP_ENDPOINT:?Set DBS_SOAP_ENDPOINT}
            MAIL_API_URL: ${MAIL_API_URL:-}
            MAIL_AUTH_KEY: ${MAIL_AUTH_KEY:-}
            MAIL_ADAPTER_KEY: ${MAIL_ADAPTER_KEY:-}
            MAIL_TO_TARGET: ${MAIL_TO_TARGET:-}
        volumes:
            - ./Caddyfile:/etc/caddy/Caddyfile:ro
            - ./sites:/etc/caddy/sites:ro
            - caddy_data:/data
            - caddy_config:/config
        networks:
            - proxy

volumes:
    caddy_data:
    caddy_config:

networks:
    proxy:
        external: true
```

### `.env`

Copy these values from the environment example into `.env` and fill in your SOAP credentials. For partner accounts, use the full `PARTNER-ID/SOAP-USER` login. Use the HTTPS endpoint assigned to your account and wrap values containing `$` or `#` in single quotes. Set `CADDY_IMAGE` to a version or digest that matches the configuration files for repeatable upgrades and rollback. [.env.example](.env.example)

```dotenv
DBS_SOAP_USERNAME=
DBS_SOAP_PASSWORD=
DBS_SOAP_ENDPOINT=https://soap.domain-bestellsystem.de/soap.php
# CADDY_IMAGE=ghcr.io/rkstraessler/caddy-domainbestellsystem:latest

# Optional certificate renewal alerts. Set all four values to enable them.
MAIL_API_URL=
MAIL_AUTH_KEY=
MAIL_ADAPTER_KEY=
MAIL_TO_TARGET=
```

### `Caddyfile`

```caddyfile
{
	auto_https disable_redirects
	acme_ca https://acme-v02.api.letsencrypt.org/directory

	log certificate_renewal_mail {
		output certificate_renewal_mail
		format json {
			time_format rfc3339
		}
		level ERROR
		include tls
	}
}

import sites/*.caddy
```

### `sites/api.caddy`

Create `sites/` and save one `.caddy` file per site. In this example, replace `api.example.com` and `anonymizer:3000` with your public hostname and upstream address.

```caddyfile
api.example.com {
	tls {
		dns domain_bestellsystem
	}

	reverse_proxy anonymizer:3000
}
```

Public DNS must serve the challenge TXT record from the authoritative zone. An internal DNS view can point the application hostname at a private Caddy address while Let's Encrypt resolves the challenge through public DNS. When split DNS interferes with zone discovery, set `resolvers` in the `tls` block to recursive resolvers that can see public DNS. [Caddy DNS challenge options](https://caddyserver.com/docs/caddyfile/directives/tls#syntax)

### Start Caddy

Attach upstream containers to the shared `proxy` network and make TCP 443 reachable by clients.

```sh
docker network inspect proxy >/dev/null 2>&1 || docker network create proxy
docker compose up -d
docker compose ps
```

Caddy stores certificates and ACME account data in `caddy_data`. Preserve that volume when updating the stack.

### Komodo

Deploy the same `compose.yaml` as a stack, keep `Caddyfile` and `sites/` beside it, and enter the `.env` values in the stack environment.

### Certificate renewal alerts

Set all four variables in the Compose environment to enable alerts. Leave them empty to disable email. Use an image that includes `caddy.logging.writers.certificate_renewal_mail` and copy the global `log certificate_renewal_mail` block if you maintain your own Caddyfile. [Caddyfile](Caddyfile) · [REIKEM Mail Go client](https://github.com/rkstraessler/ReikemMailClient_GO/tree/v1.0.0)

| Variable | Value |
| --- | --- |
| `MAIL_API_URL` | HTTPS base URL of the REIKEM Mail API, such as `https://mail.example.org` |
| `MAIL_AUTH_KEY` | Full API authentication key |
| `MAIL_ADAPTER_KEY` | Key for the sending adapter |
| `MAIL_TO_TARGET` | One recipient email address |

Caddy emails the affected host, UTC timestamp, and final error when an automatic background renewal ends in failure. Periodic maintenance uses a 30-day retry budget before reporting a terminal failure. A renewal that succeeds on retry sends no email. If the mail API rejects the message or is unreachable, Caddy logs the send error.

## Build and test

Requires Go 1.27.4 and Docker with Compose v2. The Dockerfile uses the official Caddy 2.11.9 Alpine builder and runtime images, pinned by digest. The module uses libdns 1.1.1. The image includes dependency license notices under `/usr/share/licenses/caddy-domainbestellsystem/`.

```sh
go test -race ./...
docker build -t dbs-caddy:local .
docker run --rm --network none dbs-caddy:local caddy list-modules --skip-standard
```

The tests use a local TLS SOAP mock based on the vendor WSDL and DNS examples. CI verifies the Go source, race behavior, dependencies, the Caddy image and Caddyfile, and scans the built executable with govulncheck. For a separately authorized live test, configure a disposable public domain and a separate Caddy data volume, then set the global `acme_ca` to `https://acme-staging-v02.api.letsencrypt.org/directory`. Confirm that issuance succeeds, both ordinary and wildcard challenges work, and the TXT records are removed. Switch back to the production directory and data volume for deployment. [Let's Encrypt staging](https://letsencrypt.org/docs/staging-environment/)

## GitHub setup

A repository administrator applies these settings when setting up a new repository.

1. Push the initial project to `main` and set it as the default branch. Enable squash merging, native auto-merge, automatic deletion of merged branches, dependency graph and Dependabot alerts/security updates, and private vulnerability reporting. Keep workflow permissions read-only by default. Enable **Allow GitHub Actions to create and approve pull requests** for the automation described below, subject to organization policy.
2. Provision the labels before enabling scheduled dependency updates with `bash .github/scripts/sync-labels.sh`. This updates the listed labels in place. Dependabot runs daily at 03:00 Europe/Berlin using its native timezone option. It groups compatible Go and Actions updates and groups both Caddy base images. GitHub Dependabot rebases open updates when the base changes. [Dependabot options](https://docs.github.com/en/code-security/reference/supply-chain-security/dependabot-options-reference)
3. Create an **active branch ruleset** targeting `main`. Require a pull request with one approval, dismiss stale approvals, require conversation resolution, allow squash only, and require the **Required CI** check from GitHub Actions with **Require branches to be up to date before merging**. Do not enable **Require review from Code Owners** if automated Dependabot approval is needed. `CODEOWNERS` requests review from the repository owner. Keep human write access with the administrators if every ordinary approval must come from them. Give only the repository-admin role **Always allow** bypass on this ruleset.
4. Use a second active ruleset on `main` to restrict deletions and block force pushes, with no bypass actors. Protect release tags `v*` with a tag ruleset restricting creation to administrators and blocking updates and deletions.
5. Install the official [autofix.ci app](https://github.com/apps/autofix-ci) for this repository. The workflow formats first-party Go files on PRs. Branch rules require CI for the resulting commit. Install the app before relying on automatic fixes.
6. For the first release, create an annotated SemVer tag on a verified commit already on `main`. The release workflow verifies the tagged source and publishes the same checked image to GHCR, then generates GitHub release notes. Stable releases update the major, minor, and `latest` aliases only when newer for that channel. Prereleases get their full version tag only. Make the GHCR package public after it is created and verify that an unauthenticated pull succeeds. [GitHub package visibility](https://docs.github.com/en/packages/learn-github-packages/configuring-a-packages-access-control-and-visibility)

The merge workflow accepts verified Dependabot metadata for the current signed patch/minor commit and waits for required checks before submitting a commit-specific review and requesting native squash auto-merge. It gets no ruleset bypass. Major updates and unclassified changes require manual review. If organization policy blocks GitHub Actions approvals or requires a code-owner review, keep the protection and approve the eligible PR manually. GitHub rulesets cannot express a semver-specific review bypass. [GitHub ruleset rules](https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/managing-rulesets/available-rules-for-rulesets) · [Dependabot automation](https://docs.github.com/en/code-security/tutorials/secure-your-dependencies/automate-dependabot-with-actions)

The branch update workflow merges new commits from `main` into conflict-free pull request branches in this repository and triggers the required CI run on the updated branch. Install a GitHub App with write access to **Contents** and **Pull requests** for this repository. Set `PR_AUTOMATION_SLUG` as a repository variable. In the `pr-branch-updates` environment, restrict deployments to `main`, set the `PR_AUTOMATION_CLIENT_ID` variable, and add the `PR_AUTOMATION_PRIVATE_KEY` secret. [GitHub App authentication](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/making-authenticated-api-requests-with-a-github-app-in-a-github-actions-workflow)

## Troubleshooting

- **HTTP 401 or SOAP authentication error** — Verify the full Basic Auth login, SOAP account permissions, and endpoint in Komodo. Errors report status or return codes with credentials and server-supplied message text omitted.
- **SOAP code 2303 during zone lookup** — Check which public zone is authoritative and whether the SOAP account manages it. For CNAME delegation, configure `dns_challenge_override_domain` with the complete challenge target name in a zone the account can modify.
- **Propagation timeout** — Inspect the TXT record through public DNS and at authoritative nameservers. Adjust Caddy's `propagation_timeout` if needed. Reads retry bounded transient failures. Writes execute once per call because the vendor contract does not provide a write idempotency key. If a response is lost, inspect the zone before removing any stale challenge record.
- **Parallel challenges** — Individual vendor operations preserve other records, including different values at the same name. In-process operations for a zone are serialized. Separate Caddy processes need coordinated certificate storage if they manage the same certificates. The vendor exposes neither record IDs nor a compare-and-delete operation for independently recreating an identical record.

The provider source is MIT-licensed, copyright REIKEM. Caddy and libdns retain their upstream licenses. [MIT license](LICENSE) · [Caddy license](https://github.com/caddyserver/caddy/blob/v2.11.9/LICENSE) · [libdns license](https://github.com/libdns/libdns/blob/v1.1.1/LICENSE)
