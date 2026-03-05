#!/usr/bin/env bash
set -euo pipefail

HTMX_VERSION="2.0.8"

refresh_htmx() {
    mkdir -p static/vendor
    curl -sL "https://unpkg.com/htmx.org@${HTMX_VERSION}/dist/htmx.min.js" \
        -o static/vendor/htmx.min.js
    echo "Downloaded htmx ${HTMX_VERSION}"
}

case "${1:-build}" in
    build)
        templ generate
        go build -o thought-store .
        ;;
    test)
        templ generate
        go test ./... -v
        ;;
    --refresh-htmx)
        refresh_htmx
        ;;
    *)
        echo "Usage: ./build.sh [build|test|--refresh-htmx]"
        exit 1
        ;;
esac
