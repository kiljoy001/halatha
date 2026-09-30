#!/usr/bin/env python3
"""
patch-guesscpuhz.py: Patches the 9legacy Plan 9 kernel to prevent guesscpuhz zero-MHz panic in v86.
Overwrites the panic call on zero measurement with a fallback of m->cpumhz = 1000.
Can patch a raw disk image (.img) or a standalone kernel binary/gzip.
"""

import sys
import struct
import gzip
import re
from pathlib import Path

# In guesscpuhz(), m->cpumhz is loaded at [ebx + 0xc4]:
# 8b 83 c4 00 00 00 : mov eax, [ebx + 0xc4]
# Followed by panic check (18 bytes):
# 83 f8 00 75 0d b8 <addr> 89 04 24 e8 <rel>
# Replaced with fallback (18 bytes):
# 85 c0 75 0a c7 83 c4 00 00 00 e8 03 00 00 90 90 90 90 (test eax,eax; jnz +10; mov [ebx+0xc4], 1000; nops)
ANCHOR = bytes.fromhex('8b 83 c4 00 00 00')
PANIC_PREFIX = bytes.fromhex('83 f8 00 75 0d')
PATCH_BYTES = bytes.fromhex('85 c0 75 0a c7 83 c4 00 00 00 e8 03 00 00 90 90 90 90')

def patch_kernel_bytes(data: bytes) -> bytes:
    # Check if gzipped
    is_gz = data[:2] == b'\x1f\x8b'
    if is_gz:
        raw = gzip.decompress(data)
    else:
        raw = data

    anchor_idx = raw.find(ANCHOR)
    if anchor_idx < 0:
        raise ValueError("Could not find m->cpumhz load anchor in kernel.")

    target_off = anchor_idx + len(ANCHOR)
    curr_bytes = raw[target_off : target_off + 18]

    if curr_bytes == PATCH_BYTES:
        print("Kernel is already patched.")
        return data

    if not curr_bytes.startswith(PANIC_PREFIX):
        raise ValueError(f"Found anchor at 0x{anchor_idx:x}, but bytes at 0x{target_off:x} ({curr_bytes.hex(' ')}) do not match panic check.")

    print(f"Found guesscpuhz panic at offset 0x{target_off:x}. Applying 18-byte fallback patch...")
    patched = raw[:target_off] + PATCH_BYTES + raw[target_off + 18:]

    if is_gz:
        return gzip.compress(patched, compresslevel=9)
    return patched

def patch_image(img_path: Path):
    print(f"Inspecting disk image: {img_path}")
    with open(img_path, 'r+b') as f:
        mbr = f.read(512)
        # Find Plan 9 partition (type 0x39)
        plan9_start = 0
        for i in range(4):
            ent = mbr[446 + i * 16 : 446 + (i + 1) * 16]
            if ent[4] == 0x39:
                plan9_start = struct.unpack('<I', ent[8:12])[0]
                break

        if not plan9_start:
            raise ValueError("No Plan 9 MBR partition found in image.")

        # Read prep partition table
        f.seek(plan9_start * 512)
        prep = f.read(64 * 1024).decode('latin1', errors='ignore')
        fat_start = fat_end = None
        for line in prep.splitlines():
            idx = line.find('part ')
            if idx >= 0:
                line = line[idx:]
            parts = line.strip().split()
            if len(parts) >= 4 and parts[0] == 'part' and parts[1] == '9fat':
                fat_start = int(parts[2])
                fat_end = int(parts[3])
                break

        if fat_start is None:
            raise ValueError("Could not find 9fat partition in prep table.")

        fat_offset = (plan9_start + fat_start) * 512
        fat_size = (fat_end - fat_start) * 512

        f.seek(fat_offset)
        fat = bytearray(f.read(fat_size))

        # Parse FAT16 parameters
        bps = struct.unpack('<H', fat[11:13])[0]
        spc = fat[13]
        reserved = struct.unpack('<H', fat[14:16])[0]
        nfats = fat[16]
        root_entries = struct.unpack('<H', fat[17:19])[0]
        fat_sectors = struct.unpack('<H', fat[22:24])[0]

        root_off = (reserved + nfats * fat_sectors) * bps
        root_len = ((root_entries * 32 + bps - 1) // bps) * bps
        data_off_base = root_off + root_len
        cluster_size = bps * spc

        # Locate 9PCWANIX.GZ
        kernel_dir_off = None
        kernel_cluster = None
        kernel_size = None
        for off in range(root_off, root_off + root_len, 32):
            if fat[off] == 0x00:
                break
            if fat[off] == 0xe5:
                continue
            name = fat[off:off + 11].decode('latin1')
            if '9PCWANIX' in name or '9PCCPU' in name:
                kernel_dir_off = off
                kernel_cluster = struct.unpack('<H', fat[off + 26 : off + 28])[0]
                kernel_size = struct.unpack('<I', fat[off + 28 : off + 32])[0]
                print(f"Found kernel in 9fat: {name.strip()} (Cluster: {kernel_cluster}, Size: {kernel_size} bytes)")
                break

        if not kernel_dir_off:
            raise ValueError("Could not find kernel file in 9fat.")

        # Read kernel payload
        is_fat12 = b'FAT12' in fat[54:62]
        print(f"FAT format: {'FAT12' if is_fat12 else 'FAT16'}")

        # Read clusters
        clusters = []
        c = kernel_cluster
        end_marker = 0x0ff8 if is_fat12 else 0xfff8
        while c < end_marker and c >= 2:
            clusters.append(c)
            if is_fat12:
                fat_entry_off = reserved * bps + (c * 3) // 2
                val = struct.unpack('<H', fat[fat_entry_off : fat_entry_off + 2])[0]
                c = (val & 0x0fff) if (c % 2 == 0) else (val >> 4)
            else:
                fat_entry_off = reserved * bps + c * 2
                c = struct.unpack('<H', fat[fat_entry_off : fat_entry_off + 2])[0]

        kernel_bytes = bytearray()
        for cl in clusters:
            c_off = data_off_base + (cl - 2) * cluster_size
            kernel_bytes.extend(fat[c_off : c_off + cluster_size])
        kernel_data = bytes(kernel_bytes[:kernel_size])

        # Patch kernel
        patched_kernel = patch_kernel_bytes(kernel_data)
        if patched_kernel == kernel_data:
            print("Image kernel is already patched.")
            return

        max_capacity = len(clusters) * cluster_size
        if len(patched_kernel) > max_capacity:
            raise ValueError(f"Patched kernel size ({len(patched_kernel)}) exceeds allocated cluster capacity ({max_capacity}).")

        # Write patched kernel back into clusters
        for i, cl in enumerate(clusters):
            c_off = data_off_base + (cl - 2) * cluster_size
            chunk = patched_kernel[i * cluster_size : (i + 1) * cluster_size]
            if len(chunk) < cluster_size:
                chunk = chunk + b'\x00' * (cluster_size - len(chunk))
            fat[c_off : c_off + cluster_size] = chunk

        # Update file size in directory entry
        struct.pack_into('<I', fat, kernel_dir_off + 28, len(patched_kernel))

        # Write FAT back to disk image
        f.seek(fat_offset)
        f.write(fat)
        print(f"Successfully wrote patched kernel back to {img_path} (new size: {len(patched_kernel)} bytes).")

if __name__ == '__main__':
    if len(sys.argv) < 2:
        print(f"Usage: {sys.argv[0]} <image.img | kernel.gz>")
        sys.exit(1)

    target = Path(sys.argv[1])
    if not target.exists():
        print(f"Error: {target} does not exist.")
        sys.exit(1)

    if target.suffix == '.img':
        patch_image(target)
    else:
        data = target.read_bytes()
        patched = patch_kernel_bytes(data)
        target.write_bytes(patched)
        print(f"Successfully patched {target}.")
