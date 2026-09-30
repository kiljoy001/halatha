#!/usr/bin/env python3
"""
build_libtab.py — Extract 9legacy binaries and generate signed libtab manifest.
Conforms to 9lx libtab specification (RFC 4648 §5 urlsafe base64, Ed25519 signature, ndb syntax).
"""

import os
import sys
import shutil
import hashlib
import base64
import tarfile
from pathlib import Path
from cryptography.hazmat.primitives.asymmetric import ed25519
from cryptography.hazmat.primitives import serialization

# Everything lives inside the checkout: this script is extras/9legacy/.
BASE_DIR = Path(__file__).resolve().parent
REPO_DIR = BASE_DIR.parent.parent
EXTRACT_SRC = Path("/tmp/9legacy-extract")
TARGET_BIN = BASE_DIR / "bin"
KEY_PRIV = BASE_DIR / "halatha-root.priv"
KEY_PUB = BASE_DIR / "halatha-root.pub"
KEY_PUB_HEX = BASE_DIR / "halatha-root.pub.hex"
OUTPUT_TAB = BASE_DIR / "bin.tab"
OUTPUT_TAR = BASE_DIR / "bin.tar.gz"

# Extra binaries that are not part of the 9legacy extract but must be in the
# demand-loaded tree. Yggdrasil lives here rather than in the disk image: the
# two binaries are ~18MB uncompressed, which is most of the gzroot budget and
# was the reason the fast 16MB-base image shipped with no mesh at all.
EXTRA_BINARIES = [
    (REPO_DIR / "local/yggdrasil-plan9-386/yggdrasil", "yggdrasil"),
    (REPO_DIR / "local/yggdrasil-plan9-386/yggdrasilctl", "yggdrasilctl"),
]


def setup_directories():
    BASE_DIR.mkdir(parents=True, exist_ok=True)
    # Only wipe the tree when we can actually rebuild it. Without the extract
    # dir a wipe would silently destroy 803 staged binaries and leave the
    # manifest describing files that no longer exist.
    if EXTRACT_SRC.exists():
        if TARGET_BIN.exists():
            shutil.rmtree(TARGET_BIN)
    elif TARGET_BIN.exists():
        print(f"[*] {EXTRACT_SRC} absent; keeping existing {TARGET_BIN}")
    TARGET_BIN.mkdir(parents=True, exist_ok=True)

    # 1. Copy 386/bin (native binaries)
    src_386 = EXTRACT_SRC / "386" / "bin"
    if src_386.exists():
        for root, dirs, files in os.walk(src_386):
            rel = Path(root).relative_to(src_386)
            dst_dir = TARGET_BIN / rel
            dst_dir.mkdir(parents=True, exist_ok=True)
            for f in files:
                src_file = Path(root) / f
                dst_file = dst_dir / f
                shutil.copy2(src_file, dst_file)
                os.chmod(dst_file, 0o755)

    # 2. Copy rc/bin (scripts) without overwriting existing binaries
    src_rc = EXTRACT_SRC / "rc" / "bin"
    if src_rc.exists():
        for root, dirs, files in os.walk(src_rc):
            rel = Path(root).relative_to(src_rc)
            dst_dir = TARGET_BIN / rel
            dst_dir.mkdir(parents=True, exist_ok=True)
            for f in files:
                src_file = Path(root) / f
                dst_file = dst_dir / f
                if not dst_file.exists():
                    shutil.copy2(src_file, dst_file)
                    os.chmod(dst_file, 0o755)

    # Stage the extras regardless of whether the extract dir was present.
    for src, name in EXTRA_BINARIES:
        if not src.exists():
            print(f"[!] extra binary missing, skipping: {src}")
            continue
        dst = TARGET_BIN / name
        if not dst.exists() or dst.stat().st_size != src.stat().st_size:
            shutil.copy2(src, dst)
            os.chmod(dst, 0o755)
            print(f"[*] Staged extra binary: {name} ({src.stat().st_size} bytes)")

    print(f"[*] Staged 9legacy binaries in {TARGET_BIN}")

def get_or_generate_keys():
    if KEY_PRIV.exists() and KEY_PUB.exists():
        with open(KEY_PRIV, "rb") as f:
            priv = ed25519.Ed25519PrivateKey.from_private_bytes(f.read())
        with open(KEY_PUB, "rb") as f:
            pub = ed25519.Ed25519PublicKey.from_public_bytes(f.read())
        print(f"[*] Loaded existing signing keys from {KEY_PRIV}")
    else:
        priv = ed25519.Ed25519PrivateKey.generate()
        pub = priv.public_key()
        raw_priv = priv.private_bytes(
            serialization.Encoding.Raw,
            serialization.PrivateFormat.Raw,
            serialization.NoEncryption()
        )
        raw_pub = pub.public_bytes(
            serialization.Encoding.Raw,
            serialization.PublicFormat.Raw
        )
        with open(KEY_PRIV, "wb") as f:
            f.write(raw_priv)
        with open(KEY_PUB, "wb") as f:
            f.write(raw_pub)
        with open(KEY_PUB_HEX, "w") as f:
            f.write(raw_pub.hex() + "\n")
        print(f"[*] Generated fresh Ed25519 keypair in {BASE_DIR}")

    raw_pub = pub.public_bytes(
        serialization.Encoding.Raw,
        serialization.PublicFormat.Raw
    )
    with open(KEY_PUB_HEX, "w") as f:
        f.write(raw_pub.hex() + "\n")
    print(f"[*] Public Key (hex): {raw_pub.hex()}")
    return priv, pub, raw_pub

def b64url(data: bytes) -> str:
    return base64.urlsafe_b64encode(data).decode("ascii")

def build_libtab(priv, pub):
    files_to_index = []
    for root, dirs, files in os.walk(TARGET_BIN):
        dirs.sort()
        for f in sorted(files):
            full_path = Path(root) / f
            rel_path = full_path.relative_to(TARGET_BIN).as_posix()
            files_to_index.append((rel_path, full_path))

    files_to_index.sort(key=lambda x: x[0])
    print(f"[*] Indexing & signing {len(files_to_index)} binaries...")

    lines = [
        "# libtab 9legacy binary manifest",
        "# format: ndb syntax with typed cells (HASHED, SIGNED)",
        "schema=binaries",
        "\tcol=path",
        "\tcol=size",
        "\tcol=sha256",
        "\tcol=attest\ttype=SIGNED\tsigner=halatha-root",
        ""
    ]

    verified = 0
    for rel_path, full_path in files_to_index:
        data = full_path.read_bytes()
        size = len(data)
        sha256 = hashlib.sha256(data).hexdigest()

        # Canonical body string for signature
        body_str = f"path={rel_path} size={size} sha256={sha256}"
        body_bytes = body_str.encode("utf-8")
        sig = priv.sign(body_bytes)

        # Verify signature immediately
        pub.verify(sig, body_bytes)
        verified += 1

        body_b64 = b64url(body_bytes)
        sig_b64 = b64url(sig)
        attest_cell = f"signed:{body_b64}:{sig_b64}"

        lines.append(f"path={rel_path}")
        lines.append(f"\tsize={size}")
        lines.append(f"\tsha256={sha256}")
        lines.append(f"\tattest={attest_cell}")
        lines.append("")

    tab_content = "\n".join(lines)
    with open(OUTPUT_TAB, "w", encoding="utf-8") as f:
        f.write(tab_content)

    # Also place a copy inside bin/ for guest access
    shutil.copy2(OUTPUT_TAB, TARGET_BIN / "bin.tab")
    shutil.copy2(KEY_PUB_HEX, TARGET_BIN / "halatha-root.pub")

    print(f"[*] Created {OUTPUT_TAB} ({verified} entries, {len(tab_content)} bytes)")

def package_archive():
    print(f"[*] Packaging {OUTPUT_TAR}...")
    with tarfile.open(OUTPUT_TAR, "w:gz") as tar:
        # Add bin.tab at the archive root
        tar.add(OUTPUT_TAB, arcname="bin.tab")
        tar.add(KEY_PUB_HEX, arcname="halatha-root.pub")
        # Add all files under bin/
        for root, dirs, files in os.walk(TARGET_BIN):
            for f in files:
                full_path = Path(root) / f
                arc_name = f"bin/{full_path.relative_to(TARGET_BIN).as_posix()}"
                tar.add(full_path, arcname=arc_name)
    print(f"[*] Archive ready: {OUTPUT_TAR} ({OUTPUT_TAR.stat().st_size} bytes)")

def main():
    setup_directories()
    priv, pub, raw_pub = get_or_generate_keys()
    build_libtab(priv, pub)
    package_archive()
    print("[+] All tasks completed successfully.")

if __name__ == "__main__":
    main()
