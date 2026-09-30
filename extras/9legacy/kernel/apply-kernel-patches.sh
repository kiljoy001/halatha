#!/bin/sh
# apply-kernel-patches.sh: Apply Halatha kernel drivers and configuration to a 9legacy source tree.
# Usage: ./apply-kernel-patches.sh [path-to-9legacy-repo]

set -eu

REPO_DIR="${1:-$HOME/Repo/9legacy}"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PATCH_DIR="$(cd "$SCRIPT_DIR/../patches" && pwd)"

if [ ! -d "$REPO_DIR/sys/src/9/pc" ]; then
    echo "Error: $REPO_DIR does not appear to be a 9legacy source tree (missing sys/src/9/pc)" >&2
    exit 1
fi

echo "Applying Halatha kernel modifications to $REPO_DIR..."

# Copy drivers and config
cp -v "$SCRIPT_DIR/devvirtio9p.c" "$REPO_DIR/sys/src/9/pc/devvirtio9p.c"
cp -v "$SCRIPT_DIR/usbns.c" "$REPO_DIR/sys/src/9/pc/usbns.c"
cp -v "$SCRIPT_DIR/pcwanix" "$REPO_DIR/sys/src/9/pc/pcwanix"

# Copy pre-generated elliptic curve tables for libsec
mkdir -p "$REPO_DIR/sys/src/libsec/port"
cp -v "$SCRIPT_DIR/jacobian.c" "$REPO_DIR/sys/src/libsec/port/jacobian.c"
cp -v "$SCRIPT_DIR/secp256r1.c" "$REPO_DIR/sys/src/libsec/port/secp256r1.c"
cp -v "$SCRIPT_DIR/secp384r1.c" "$REPO_DIR/sys/src/libsec/port/secp384r1.c"

# Apply mkfile patches if needed
if ! grep -q "usbns.\$O" "$REPO_DIR/sys/src/9/pc/mkfile"; then
    echo "Patching $REPO_DIR/sys/src/9/pc/mkfile..."
    sed -i 's/devusb.\$O usbuhci.\$O usbohci.\$O usbehci.\$O: ..\/port\/usb.h/devusb.\$O usbuhci.\$O usbohci.\$O usbehci.\$O usbns.\$O: ..\/port\/usb.h/' "$REPO_DIR/sys/src/9/pc/mkfile"
fi

if ! grep -q "mpc absent: keep the committed" "$REPO_DIR/sys/src/libsec/port/mkfile"; then
    echo "Patching $REPO_DIR/sys/src/libsec/port/mkfile..."
    if [ -f "$PATCH_DIR/mkfile-libsec.patch" ]; then
        (cd "$REPO_DIR" && patch -p1 < "$PATCH_DIR/mkfile-libsec.patch" || true)
    fi
fi

echo "Halatha kernel source and configuration applied successfully to $REPO_DIR."
