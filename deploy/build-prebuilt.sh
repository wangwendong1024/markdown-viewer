#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
: "${GO_BIN:?Set GO_BIN to the absolute Go executable}"
: "${DOCKER_BIN:=/usr/local/bin/docker}"
cd ui
yarn install --frozen-lockfile --non-interactive
NODE_OPTIONS=--openssl-legacy-provider yarn build
cd ..
if [ -e bindata.go ]; then
    echo 'bindata.go already exists; preserve it and use a clean build checkout.' >&2
    exit 1
fi
cp deploy/assets.go.txt bindata.go
trap 'rm -f bindata.go' EXIT
mkdir -p dist/docker
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 "$GO_BIN" build -trimpath -ldflags='-s -w' -o dist/docker/markdown-viewer .
cp deploy/Dockerfile.prebuilt dist/docker/Dockerfile
"$DOCKER_BIN" build --pull=false -t markdown-viewer:local dist/docker
