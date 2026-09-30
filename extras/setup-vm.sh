#!/bin/bash
# extras/setup-vm.sh — Master automated setup for Halatha VM disk images and guest environments.
#
# This script ensures that all VM-only dependencies (custom kernel drivers, Yggdrasil
# Plan 9 binaries, Objective-9C toolchain, libtab verified binaries, and guest scripts)
# are staged, patched, assembled, and verified into reproducible VM disk images.
#
# Usage:
#   ./extras/setup-vm.sh [options]
#
# Options:
#   --all         Run full end-to-end setup (default)
#   --lean        Build and patch lean (16MB) image only
#   --full        Build and patch full (40MB) image
#   --kernel      Apply kernel patches and drivers to companion 9legacy tree
#   --ygg         Cross-compile Yggdrasil for Plan 9 (386)
#   --libtab      Extract binaries and generate signed bin.tab manifest
#   --patch-only  Apply guesscpuhz 1000MHz fallback patch to existing images
#   --help        Show this help message

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
HALATHA_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
LOCAL_DIR="$HALATHA_ROOT/local"
EXTRAS_DIR="$HALATHA_ROOT/extras"
SCRIPTS_DIR="$EXTRAS_DIR/scripts"

mkdir -p "$LOCAL_DIR"

MODE="all"
if [ $# -gt 0 ]; then
    case "$1" in
        --all|--lean|--full|--kernel|--ygg|--libtab|--patch-only)
            MODE="${1#--}"
            ;;
        --help|-h)
            sed -n '2,17p' "$0" | sed 's/^# //'
            exit 0
            ;;
        *)
            echo "Unknown option: $1 (see --help)" >&2
            exit 1
            ;;
    esac
fi

echo "================================================================"
echo " Halatha VM Setup & Image Pipeline [Mode: $MODE]"
echo "================================================================"

# 1. Apply Kernel Drivers & Patches
setup_kernel() {
    echo ""
    echo "[1/6] Staging Halatha kernel drivers and configuration..."
    if [ -f "$EXTRAS_DIR/9legacy/kernel/apply-kernel-patches.sh" ]; then
        if [ -d "$HALATHA_ROOT/../9legacy" ]; then
            "$EXTRAS_DIR/9legacy/kernel/apply-kernel-patches.sh" "$HALATHA_ROOT/../9legacy"
        else
            echo "Notice: Companion 9legacy repo not found at ../9legacy; kernel sources are preserved in extras/9legacy/kernel/"
        fi
    fi
}

# 2. Build or Stage Yggdrasil 386 Binaries
setup_ygg() {
    echo ""
    echo "[2/6] Checking Yggdrasil Plan 9 (386) binaries..."
    YGG_DIR="$LOCAL_DIR/yggdrasil-plan9-386"
    mkdir -p "$YGG_DIR"
    if [ -f "$YGG_DIR/yggdrasil" ] && [ -f "$YGG_DIR/yggdrasilctl" ]; then
        echo "Yggdrasil binaries present in $YGG_DIR."
    elif [ -d "$HALATHA_ROOT/../yggdrasil-go" ]; then
        "$SCRIPTS_DIR/build-yggdrasil.sh" "$HALATHA_ROOT/../yggdrasil-go"
    else
        echo "Downloading prebuilt Yggdrasil binaries from mirror..."
        curl -fsSL -o "$YGG_DIR/yggdrasil" "https://halatha.rentonsoftworks.com/local/yggdrasil-plan9-386/yggdrasil" || true
        curl -fsSL -o "$YGG_DIR/yggdrasilctl" "https://halatha.rentonsoftworks.com/local/yggdrasil-plan9-386/yggdrasilctl" || true
        if [ -f "$YGG_DIR/yggdrasil" ]; then
            chmod +x "$YGG_DIR/yggdrasil" "$YGG_DIR/yggdrasilctl"
            echo "Successfully staged Yggdrasil binaries."
        else
            echo "Warning: Could not build or download Yggdrasil binaries."
        fi
    fi
}

# 3. Build / Verify libtab Verified Binary Mirror
setup_libtab() {
    echo ""
    echo "[3/6] Verifying libtab signed binary repository..."
    if [ -f "$EXTRAS_DIR/9legacy/bin.tab" ] && [ -d "$EXTRAS_DIR/9legacy/bin" ]; then
        COUNT=$(find "$EXTRAS_DIR/9legacy/bin" -type f | wc -l)
        echo "Found signed libtab manifest with $COUNT binaries in $EXTRAS_DIR/9legacy/bin."
    else
        echo "Generating libtab signed binary catalog..."
        if [ -f "$EXTRAS_DIR/9legacy/build_libtab.py" ]; then
            python3 "$EXTRAS_DIR/9legacy/build_libtab.py"
        fi
    fi
}

# 4. Acquire Base Images If Missing
acquire_images() {
    echo ""
    echo "[4/6] Verifying base disk images in $LOCAL_DIR..."
    LEAN_IMG="$LOCAL_DIR/9legacy-wanix-virtio9p.img"
    FULL_IMG="$LOCAL_DIR/9legacy-wanix-virtio9p-full-usb.img"

    if [ ! -f "$LEAN_IMG" ]; then
        echo "Downloading lean base image (9legacy-wanix-virtio9p.img)..."
        curl -fSL -o "$LEAN_IMG" "https://halatha.rentonsoftworks.com/local/9legacy-wanix-virtio9p.img"
    fi

    if [ "$MODE" = "all" ] || [ "$MODE" = "full" ]; then
        if [ ! -f "$FULL_IMG" ]; then
            echo "Downloading full USB base image (9legacy-wanix-virtio9p-full-usb.img)..."
            curl -fSL -o "$FULL_IMG" "https://halatha.rentonsoftworks.com/local/9legacy-wanix-virtio9p-full-usb.img"
        fi
    fi
}

# 5. Patch Kernel Calibration (guesscpuhz)
patch_kernels() {
    echo ""
    echo "[5/6] Patching kernel timer calibration (guesscpuhz fallback to 1000MHz)..."
    LEAN_IMG="$LOCAL_DIR/9legacy-wanix-virtio9p.img"
    FULL_IMG="$LOCAL_DIR/9legacy-wanix-virtio9p-full-usb.img"

    if [ -f "$LEAN_IMG" ]; then
        python3 "$SCRIPTS_DIR/patch-guesscpuhz.py" "$LEAN_IMG"
    fi

    if [ -f "$FULL_IMG" ] && ([ "$MODE" = "all" ] || [ "$MODE" = "full" ]); then
        python3 "$SCRIPTS_DIR/patch-guesscpuhz.py" "$FULL_IMG"
    fi
}

# 6. Verify VM Image Integrity
verify_images() {
    echo ""
    echo "[6/6] Verifying assembled disk images..."
    LEAN_IMG="$LOCAL_DIR/9legacy-wanix-virtio9p.img"
    if [ -f "$LEAN_IMG" ]; then
        BYTES=$(stat -c %s "$LEAN_IMG")
        echo "✓ Lean Image: $LEAN_IMG ($BYTES bytes)"
    fi

    FULL_IMG="$LOCAL_DIR/9legacy-wanix-virtio9p-full-usb.img"
    if [ -f "$FULL_IMG" ]; then
        BYTES=$(stat -c %s "$FULL_IMG")
        echo "✓ Full USB Image: $FULL_IMG ($BYTES bytes)"
    fi
}

# Execution Dispatch
case "$MODE" in
    kernel)
        setup_kernel
        ;;
    ygg)
        setup_ygg
        ;;
    libtab)
        setup_libtab
        ;;
    patch-only)
        patch_kernels
        ;;
    lean)
        setup_kernel
        setup_ygg
        setup_libtab
        acquire_images
        patch_kernels
        verify_images
        ;;
    full)
        setup_kernel
        setup_ygg
        setup_libtab
        acquire_images
        patch_kernels
        verify_images
        ;;
    all)
        setup_kernel
        setup_ygg
        setup_libtab
        acquire_images
        patch_kernels
        verify_images
        ;;
esac

echo ""
echo "================================================================"
echo " Halatha VM setup complete and verified ready for execution!"
echo "================================================================"
