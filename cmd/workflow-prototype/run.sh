#!/bin/sh
set -eu
cd "$(dirname "$0")"
cache="${XDG_CACHE_HOME:-${HOME}/.cache}/unreal-agent/workflow-prototype"
python_dir="$cache/python-3.14.7"
resume=false
for arg in "$@"; do
    case "$arg" in
        -resume|--resume|-resume=*|--resume=*) resume=true ;;
    esac
done
mkdir -p "$cache"
if [ "$resume" = false ]; then
if [ ! -f "$python_dir/python.wasm" ]; then
    mkdir -p "$cache"
    archive="$cache/python-3.14.7.zip"
    curl -fL 'https://github.com/brettcannon/cpython-wasi-build/releases/download/v3.14.7/python-3.14.7-wasi_sdk-24.zip' -o "$archive"
    actual=$(shasum -a 256 "$archive" | cut -d ' ' -f 1)
    [ "$actual" = 2e064d3fb8172471d39d741348efa722349c40b96301f69968dff714999c584b ] || { echo 'CPython archive checksum mismatch' >&2; exit 1; }
    mkdir -p "$python_dir"
    unzip -q -o "$archive" -d "$python_dir"
fi
./install-python-deps.sh "$python_dir" "$cache"
fi
CGO_ENABLED=0 go build -o "$cache/workflow-prototype" .
exec "$cache/workflow-prototype" -python "$python_dir" "$@"
