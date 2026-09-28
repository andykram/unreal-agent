#!/bin/sh
set -eu
cd "$(dirname "$0")"
cache="${XDG_CACHE_HOME:-${HOME}/.cache}/unreal-agent/workflow-prototype"
mkdir -p "$cache"
CGO_ENABLED=0 go build -o "$cache/workflow-prototype" .
exec "$cache/workflow-prototype" -runtime-cache "$cache" "$@"
