#!/bin/bash
# build-yggdrasil.sh: Build Yggdrasil 386 binaries for Plan 9 guests.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
HALATHA_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
TARGET_DIR="$HALATHA_ROOT/local/yggdrasil-plan9-386"
YGG_DIR="${1:-$HALATHA_ROOT/../yggdrasil-go}"

mkdir -p "$TARGET_DIR"

if [ ! -d "$YGG_DIR" ]; then
    echo "Cloning yggdrasil-go with plan9-support..."
    git clone --branch plan9-support https://github.com/yggdrasil-network/yggdrasil-go "$YGG_DIR" || {
        echo "Failed to clone yggdrasil-go" >&2
        exit 1
    }
fi

echo "Building Yggdrasil for Plan 9 (386) from $YGG_DIR..."
(
    cd "$YGG_DIR"
    GOOS=plan9 GOARCH=386 CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o "$TARGET_DIR/yggdrasil" ./cmd/yggdrasil
    GOOS=plan9 GOARCH=386 CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o "$TARGET_DIR/yggdrasilctl" ./cmd/yggdrasilctl
)

echo "Built Plan 9 binaries:"
ls -lh "$TARGET_DIR/yggdrasil" "$TARGET_DIR/yggdrasilctl"
