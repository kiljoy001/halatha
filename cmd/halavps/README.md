# Halatha VPS sessions

A stateless Harvey desktop whose system files come from an immutable GEFS
image and whose writable data lives in a per-connection ramfs. User data
leaves through the browser (save picker/download) or a WebUSB device; the
server keeps nothing.

```
VPS                                         Browser
  halavps  -- 9P2000 over WebSocket -->    Wanix import at "."
    ram/     empty mount point               |
    host/    empty mount point               v
                                         Harvey guest
                                           /        GEFS image (multiboot module)
                                           /home    local ramfs (browser)
                                           /tmp     local ramfs (browser)
                                           /var     local ramfs (browser)
                                           /n       local ramfs (browser)
                                           /n/host  local service tree (browser)
```

## Guest boot

The kernel is built with `boot/vps_init.o9`:

1. The GEFS image is a multiboot module (`initrd`). v86 copies it to guest RAM
   and Harvey exposes it as `/dev/initrd`; the kernel maps module memory
   writable, so GEFS's log and file writes stay in the guest.
2. `vps_init` runs `/boot/gefs` on `/dev/initrd`, mounts the server at `/`,
   then mounts `#9/0` (the halavps session) at `/vps`.
3. The page binds browser-local `#ramfs` over the session's `ram` and `host`
   mount points. The guest binds that local `ram/` over `/home`, `/tmp`,
   `/var`, `/n` and the local `host/` at `/n/host`. No user data crosses the
   WebSocket.
4. Rio starts from the GEFS root. Nothing is written back to the image file.

## Boot time

The page fetches the kernel and the GEFS image itself, with progress, and
hands v86 blob URLs (`initrd="blob:..."`, same for `multiboot`). This is not
cosmetic: v86's own loader reads module images through an XHR inside the v86
worker while the wasm/JIT is still coming up. On the live VPS that path
dropped from ~24 MB/s to ~1 MB/s and turned a 256 MB module into a four
minute wait. Loading on the page thread and passing a blob URL removes the
worker from the transfer path; the worker only copies the image into guest
RAM.

Two server settings matter as well:

- **HTTP/3 off.** Caddy advertises `Alt-Svc: h3` and Chrome switches to QUIC.
  From some networks QUIC collapses to under 1 MB/s for the image while TCP
  h2 stays near 20 MB/s. The Caddyfile sets `servers { protocols h1 h2 }`.
- **Immutable images.** The page version-tags the image URLs
  (`?v=20261031`) and Caddy marks `/vps-test/local/aspera/*` immutable, so a
  reload comes from the browser cache.

Measured on the live site: ~45 s from navigation to a usable Rio session
(cold), ~35 s on reload, down from ~5 minutes. Rough breakdown: ~12 s page and
Wanix startup, ~10 s image download, ~14 s wasm compile and v86 init,
~20 s guest boot (GEFS mount, pivot, binds, Rio).

## Build

In the Harvey checkout:

```sh
# Kernel used to create/refresh the GEFS image (stock init with tar install).
python3 util/9vm/build_kernel.py

# Kernel used by the browser (VPS init).
HARVEY_GEFS_INIT=boot/vps_init.o9 \
HARVEY_GEFS_INIT_BIN=build/vpsinit \
HARVEY_BUILD_DIR=build/kernel-vps \
HARVEY_OUT_ELF=build/harvey-vps.elf \
HARVEY_OUT_32BIT=build/harvey-vps.32bit \
python3 util/9vm/build_kernel.py

# GEFS image from a staged root (the full browser rootfs).
python3 util/9vm/install_gefs.py \
    -o build/aspera-vps.gefs -s 256 \
    --root /path/to/halatha/local/aspera/rootfs
```

Copy `build/harvey-vps.32bit` and `build/aspera-vps.gefs` next to the page
(`local/aspera/`). In the Halatha checkout:

```sh
(cd v86 && make)          # v86 core + task wasm
make js                   # elements (multiboot/cpu/initrd)
make wasm-go              # Wanix kernel; or make wasm with tinygo
go build ./cmd/halavps
```

## Local boot test (no browser)

The VPS guest boots from a multiboot initrd module, so 9vm can run the same
kernel and image without a browser. Build a throwaway session directory that
looks like the halavps namespace:

```sh
mkdir -p /tmp/vpssess/ram/{home,tmp,var,n/host}
mkdir -p /tmp/vpssess/host/{clip,downloads,fetch,net,uploads,usb,app}
cp build/aspera-vps.gefs /tmp/vpssess/base.gefs
```

Then boot with a tap and a DHCP server:

```sh
unshare -Urn -- ./util/9vm/9vm -m 1024 -r /tmp/vpssess \
    -initrd build/aspera-vps.gefs -net-tap tap0 \
    -c 'bootargs=local!#9/0 nobootprompt=local gefsroot=1' \
    build/harvey-vps.32bit
```

`vps_init` runs `net.rc`, which leases an address with `ipconfig` and starts
`dns` and `cs`. The console falls back to `rc -i` when there is no framebuffer,
so `csquery /net/cs 'tcp!example.com!80'` can be typed directly.

## Run

```sh
./halavps -addr 127.0.0.1:8099
```

The page `examples/basic-vm-harvey-vps.html` needs nothing from this server
except the static images: it preloads the GEFS image and kernel, and the VM's
namespace is the Wanix task namespace with browser ramfs bound at `ram/` and
`host/`. The Wisp relay at `/net` takes no token: it is stateless and
normally runs next to the browser. `?relay=wisp://host:port/net` overrides
the relay endpoint.

`/session` still serves a token-gated 9P session for pages that want a
server-supplied namespace, but the Harvey page no longer uses it.

## Browser services

`extras/halatha/vps-services.js` runs against the same namespace the guest
sees at `/n/host`:

| Path | Direction | Meaning |
|---|---|---|
| `uploads/ctl` | guest writes `open` | show file picker; result in `uploads/in`, `uploads/name` |
| `downloads/ctl` | guest writes `save NAME` | save `downloads/NAME` via picker or download |
| `fetch/ctl` | guest writes URL | response in `fetch/body` |
| `clip/text` | both | browser clipboard |
| `usb/ctl`, `usb/<id>/...` | both | WebUSB control/bulk bridge |
| `net/status` | read | session status |

The pickers and WebUSB need Chromium/Edge, HTTPS (or localhost), and a user
gesture; the guest keypress that writes the control file supplies it.

## Network

The guest NIC uses v86's **`fetch` backend**, which runs the network in the
page: DHCP, ARP, DNS and ICMP are answered by the adapter, and TCP is carried
over the browser's `fetch`. No server relay is involved, so the VPS only
serves images and assets.

The trade-off is that guest TCP is HTTP(S) over `fetch` and therefore subject
to CORS; arbitrary TCP needs a Wisp relay. `?relay=wisp://host:port` selects
one when available (for example a local `wanix wisp --listen 127.0.0.1:8098`,
with the page served over `http` or given a trusted local `wss` certificate).

## Guest images

The images are mostly zeroes (256 MiB compresses to ~8 MiB with zstd). Caddy
serves `/vps-test/local/aspera/*` from disk with `file_server
precompressed zstd gzip`, so keep sidecars next to every image:

```sh
zstd -19 -f -o aspera-vps.gefs.zst aspera-vps.gefs
gzip -9 -c aspera-vps.gefs > aspera-vps.gefs.gz
```

Refresh them whenever a new image is deployed; otherwise Caddy falls back to
the uncompressed file.

## Notes

- The GEFS image should be 160 MiB or larger. The page uses `mem="1G"` so the
  module and GEFS cache fit.
- `fs/p9p` must pass `Topen`'s mode to the underlying filesystem; without that
  fix, imported filesystems are opened read-only and GEFS cannot write.
- `halavps` stores no image and no user data. On disconnect, `ram/` and
  `host/` are gone.
