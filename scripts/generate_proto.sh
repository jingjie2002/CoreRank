#!/usr/bin/env sh
set -eu

cd "$(dirname "$0")/.."
GOPATH_BIN="$(go env GOPATH)/bin"
PATH="$GOPATH_BIN:$PATH"
export PATH

protoc \
  --go_out=. --go_opt=paths=source_relative \
  --go-grpc_out=. --go-grpc_opt=paths=source_relative \
  api/proto/rank.proto
