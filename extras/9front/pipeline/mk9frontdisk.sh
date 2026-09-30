#!/bin/sh
set -eu

out=${1:-local/9front-halatha.img}
size=${2:-2G}

if ! command -v qemu-img >/dev/null 2>&1; then
	echo "mk9frontdisk: qemu-img not found" >&2
	exit 1
fi

if test -e "$out"; then
	echo "mk9frontdisk: refusing to overwrite $out" >&2
	exit 1
fi

mkdir -p "$(dirname "$out")"
qemu-img create -f raw "$out" "$size"
bytes=$(stat -c %s "$out")
echo "$out $bytes bytes"
