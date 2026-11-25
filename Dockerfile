FROM caddy:2.11.9-builder-alpine@sha256:86e132d1d6db8850352b2dbc15f82a4cb25363ac305ee41e11405873e5931f37 AS builder
WORKDIR /module
COPY go.mod go.sum ./
RUN go mod download
COPY provider.go LICENSE ./
COPY internal/ ./internal/
RUN XCADDY_SKIP_CLEANUP=1 xcaddy build "$(go list -m -f '{{.Version}}' github.com/caddyserver/caddy/v2)" \
    --with github.com/rkstraessler/DBS-CaddyModule=/module \
    --output /usr/bin/caddy
# Chroma combines MIT and SIL OFL notices in COPYING. Preserve the reviewed
# upstream file verbatim because the classifier cannot classify the combination.
RUN cd /tmp/buildenv_* \
    && GOFLAGS='-tags=nobadger,nomysql,nopgx' CGO_ENABLED=0 \
        go run github.com/google/go-licenses/v2@v2.0.1 save . \
        --ignore caddy --ignore github.com/alecthomas/chroma/v2 \
        --save_path=/third-party-licenses \
    && chroma="$(go list -m -f '{{.Dir}}' github.com/alecthomas/chroma/v2)/COPYING" \
    && printf '%s  %s\n' 13654b052f885cb3fa47d7343e13975d9c8a64c3bcd84993c35d5eb01f22b23f "$chroma" | sha256sum -c - \
    && mkdir -p /third-party-licenses/github.com/alecthomas/chroma/v2 /third-party-licenses/go \
    && cp "$chroma" /third-party-licenses/github.com/alecthomas/chroma/v2/COPYING \
    && cp "$(go env GOROOT)/LICENSE" "$(go env GOROOT)/PATENTS" /third-party-licenses/go/

FROM caddy:2.11.9-alpine@sha256:003a97808e781aa9f7e75cc2f43d34d461099031faf5e20faefb9f61126836f0
COPY --from=builder /usr/bin/caddy /usr/bin/caddy
COPY Caddyfile /etc/caddy/Caddyfile
RUN mkdir -p /etc/caddy/sites
COPY --from=builder /third-party-licenses/ /usr/share/licenses/caddy-domainbestellsystem/third-party/
COPY LICENSE /usr/share/licenses/caddy-domainbestellsystem/LICENSE
