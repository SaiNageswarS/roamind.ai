#!/bin/bash

rm -Rf build
mkdir -p build

cd cli
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -ldflags="-s -w -X main.Version=$(git describe --tags --always 2>/dev/null || echo 'dev')" \
    -trimpath \
    -o ../build/roamind-cli .

cd ../workflows
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -ldflags="-s -w -X main.Version=$(git describe --tags --always 2>/dev/null || echo 'dev')" \
    -trimpath \
    -o ../build/roamind-workflows .

cd ../core
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -ldflags="-s -w -X main.Version=$(git describe --tags --always 2>/dev/null || echo 'dev')" \
    -trimpath \
    -o ../build/roamind-core .