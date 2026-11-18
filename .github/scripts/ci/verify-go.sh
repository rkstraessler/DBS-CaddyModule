#!/usr/bin/env bash
set -euo pipefail

unformatted=$(git ls-files '*.go' -z | xargs -0r gofmt -l)
if [[ -n "$unformatted" ]]; then
    printf 'Files requiring gofmt:\n%s\n' "$unformatted"
    exit 1
fi

go vet -mod=readonly ./...
go test -mod=readonly -race -vet=off ./...
go run honnef.co/go/tools/cmd/staticcheck@v0.8.2 ./...
go run golang.org/x/vuln/cmd/govulncheck@v1.10.0 ./...
# This actionlint release predates GitHub's documented concurrency queue key.
go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.12 \
    -ignore '^unexpected key "queue" for "concurrency" section\.'
