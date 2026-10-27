# 9front guest integration

The default direct browser page is
[`examples/basic-v86-9front-direct.html`](../../examples/basic-v86-9front-direct.html).
It boots an installed 386 9front disk with the `9pchalatha` kernel. The
kernel's `#9` virtio-9p device carries plain 9P2000 between v86 and the
browser's `fs/p9p` namespace server. `/rc/bin/termrc.local` mounts that
namespace at `/n/host` and starts the current browser-supplied
`boot/halatha.local` after `boot/seed-ready` appears. Rio starts while the
boot hook waits, and the installed hook remains available for offline boots.

The kernel also includes `usbns`, a virtual USB host controller backed by
the browser's WebUSB files. Boot-stage `usbd` would inherit a namespace
without `/n/host`, so the image sets `nousbrc=1` and starts 9front's USB
boot script from `termrc.local` after mounting the host. The installer moves
the stock `nusbrc` call out of `termrc` to avoid starting it twice.

The page exposes the same browser services as the 9legacy direct page:
clipboard, uploads and downloads, browser file pickers, WebUSB control files,
fetch, IPFS, and the verified binary tree. It seeds the guest's rc tools on
each boot. `halatha-bind` runs from glenda's profile before Rio starts so
its command binds reach Rio windows.

## Desktop startup and local time

Rio runs glenda's `riostart`, which opens the default stats and shell windows,
then runs [`desktop.rc`](desktop.rc). The browser refreshes that script at
`/n/host/boot/desktop.rc` on every boot; the image also has an installed copy
for offline boots. Edit `desktop.rc` to change which applications start. It
currently starts factotum's on-demand Rio prompt (`auth/fgui`) in the
background and opens
`games/catclock` and `bar`.

The page publishes the browser's timezone name and a Plan 9 timezone table at
`/n/host/boot/timezone-name` and `/n/host/boot/timezone`. Glenda's profile
copies the table to `/env/timezone` before starting Rio, so its windows and
programs use the user's local time. It supports daylight transitions through
2037; if a timezone needs more than the two offsets supported by Plan 9's
table format, it uses the browser's current offset.
The guest calls itself `halatha`. Its v86 RTC is UTC, so `termrc.local`
refreshes `/dev/time` from that RTC every two seconds. This avoids stock
`aux/timesync`'s local-RTC assumption and its slow correction of clock lag.
`/env/timezone` selects how the accurate clock is displayed.

The image includes `o9c`, `o9build`, its runtime library, headers, and
standard library. In a Rio shell, create an `.o9` file and run `o9build`:

```rc
echo 'main { print("hello from o9c\n"); }' >/tmp/hello.o9
o9build /tmp/hello.o9
/tmp/hello
```

## Build the browser disk

Start with an installed 9front disk as described in
[`pipeline/README.md`](pipeline/README.md), then build the kernel and image:

```sh
python3 extras/9front/kernel/build-kernel.py local/9front-halatha-v9p13.img local/kernel-build-v9p20
python3 extras/9front/pipeline/mk9frontimg.py \
  local/kernel-build-v9p20/9pchalatha \
  local/9front-halatha-v9p13.img \
  local/9front-halatha-v9p20.img
```

The image builder stages the boot hook, profile bind, and health check into
9fat, installs them in a temporary QEMU boot, and writes the final boot
configuration. It also installs `halctl` from `local/halctl.386` (or
`HALCTL_BIN`). It refuses to overwrite an existing output image. The
generated disk is 2 GiB logical size; serve it with HTTP Range support.

Open `/examples/basic-v86-9front-direct.html` to use the default image, or
pass `?iso=/local/other.img&size=<bytes>` for a different installed disk.
An upstream installer ISO can still be booted explicitly with `?iso=...`
and its actual byte size; it has no Halatha host mount until customized.

With `go run ./examples/serve.go` serving the checkout, run the guest
smoke tests:

```sh
node dev/test-9front-virtio9p-e2e.mjs http://127.0.0.1:7070/examples/basic-v86-9front-direct.html
node dev/test-clipboard-e2e.mjs http://127.0.0.1:7070/examples/basic-v86-9front-direct.html --local --page=examples/basic-v86-9front-direct.html
node dev/test-9front-usb-e2e.mjs
node dev/test-9front-keyrequest-e2e.mjs
node dev/test-9front-desktop-e2e.mjs http://127.0.0.1:7070/examples/basic-v86-9front-direct.html Asia/Kathmandu
```

The USB test uses a synthetic WebUSB device to verify that a request goes
from 9front's `usbd` through `usbns` and the browser service. Physical
device behavior still depends on browser permissions and the device class.
The keyrequest test supplies a local dummy endpoint and checks that the guest
builds the Objective-9C client with the configured address. It does not send
an access request.

`serial9p.c` remains available for guests using the older serial bridge.
The browser image above uses virtio-9p and does not need that helper.

## 9legacy (Plan 9 from Bell Labs, 386)

9legacy is an easier target than 9front for the browser path: it's 386, v86
boots it today (copy.sh ships a 16MB `9legacy.img`), and it speaks 9P2000 — so
the same `fs/p9p` host bridge works unchanged. `serial9p.c` is **libc-only** (it
inlines the `/srv` posting instead of calling lib9p), so it compiles on 9legacy
as-is; `mk` (`mkone`) works there too.

The only 9legacy difference is the boot hook: 9front's `termrc` sources
`/rc/bin/termrc.local`, but 9legacy's may not. The image builder appends the
bridge directly to the end of `/rc/bin/termrc` (it always runs at boot):

```rc
# wanix /n/host bridge over eia1
if(test -x /bin/serial9p){
	serial9p /dev/eia1 host &
	for(i in 1 2 3 4 5){ if(! test -e /srv/host) sleep 1 }
	test -e /srv/host && mount -c /srv/host /n/host &
}
```

The copy.sh image can still emit early boot/debug text on COM1. To keep 9P
clean, pass `-serial-dev /dev/eia1` when building Halatha images and enable
v86 `uart1`. The builder also removes `console=0 b115200` from `PLAN9.INI`,
adds a short `menudefault=1280x768,1` so the browser boot does not depend on
typing at the startup menu, and `*nousbboot=1` to skip the boot-time USB post.
9legacy's local boot path can still print a non-fatal `/srv/usb` warning while
probing partitions; suppressing that completely requires a rebuilt 9legacy
bootfile.
For browser boots, the builder also rewrites `/rc/bin/termrc` so network setup
runs from `/rc/bin/wanixnet` in the background when a NIC is present. That keeps
rio startup independent from DHCP/proxy delays. `usbstart` remains opt-in via
`wanixusb=1`.

Remaster flow:

1. Build a 386 Plan 9 `serial9p` binary on a Plan 9/9front system and copy it
   to the host, for example `/tmp/wanix-serial9p.386`.
2. Patch the 16MB copy.sh image from the host:
   ```sh
   GOCACHE=/tmp/go-build-wanix go run extras/9front/mk9legacyimg.go \
     -base ~/Downloads/9legacy-copysh.img \
     -serial9p /tmp/wanix-serial9p.386 \
     -serial-dev /dev/eia1 \
     -out local/9legacy-wanix.img
   ```
3. Serve `local/9legacy-wanix.img` and boot it:
	   ```html
	   <wanix-vm hda="../local/9legacy-wanix.img"
	     boot="c" mem="512M" screen start
	     style="width:100%;height:100%"></wanix-vm>
	   ```
   From 9legacy rc: `ls /n/host` lists the wanix namespace.

### Networking 9legacy under v86 (for the grid path)

9legacy has an **ne2k** driver, not virtio. Wanix disk-boot VMs default to no
NIC; give the guest an explicit NIC + relay when networking is wanted:

```html
<wanix-vm hda="…" netdev="ne2k" relay="wisp://your-wisp-relay:port"
  boot="c" mem="512M" screen start></wanix-vm>
```

Run a WISP relay (WebSocket→TCP) on a node that can reach your grid (and, if
you go that route, Yggdrasil peers). Then the guest runs your 9front/9legacy
Yggdrasil port (build it for 386 — the open item) and reaches cpu/auth/file
servers on the mesh.

#### Automating Yggdrasil + the TCP bridge at boot

Two pieces, in two places:

1. **Guest side — Yggdrasil daemon.** Build the image with `mk9legacyimg -ygg`
   (requires `/386/bin/yggdrasil` already in the base image). This installs
   `/rc/bin/halatha.local` (read from `extras/9front/halatha.local`) and bundles
   `extras/9front/yggdrasil.peers` as `/lib/yggdrasil.peers`. termrc already
   runs `halatha.local` in the background at boot (the `# halatha optional
   local boot hook` block). It waits for an IP interface to be bound
   (`/net/ipifc/0/ctl` — true under v86 fetch DHCP *or* QEMU SLIRP), then:
   - uses `/lib/yggdrasil.conf` if present (bundle one with
     `-add your.conf:/lib/yggdrasil.conf` for a stable node identity + your
     own peers), otherwise generates a fresh random config each boot
     (`yggdrasil -genconf` — random key, so each boot is a fresh node identity);
   - starts `/386/bin/yggdrasil -useconffile /lib/yggdrasil.conf` in the shared
     namespace so its `pkt` ip interface is visible to every process. The admin
     listener is `tcp://localhost:9001` (use `yggdrasilctl`); logs go to
     `/tmp/yggdrasil.log`;
   - **auto-injects public peers**: once the admin socket is up, it runs
     `yggdrasilctl addPeer` for every non-comment line of
     `/lib/yggdrasil.peers`. The bundled peer list is curated from the public
     [yggdrasil-network/public-peers](https://github.com/yggdrasil-network/public-peers)
     repo (IPv4-only `tcp://`/`tls://` — `quic://` is UDP and won't tunnel over
     the WISP TCP relay, IPv6 literals and `?key=` params trip rc globbing).
     Runtime peers are not persisted, but the script re-injects every boot, so
     the node joins the global Yggdrasil mesh with no manual config — a terminal
     for a 9p grid. Replace `extras/9front/yggdrasil.peers` with your own
     curated list (or bundle `/lib/yggdrasil.conf` with `Peers:` entries) to
     pin specific peers.

   `halatha.local` is a real rc file (not a Go raw-string const) precisely so
   it can use rc backtick command substitution (`{grep ...}`) to read the peers
   file — backticks would close a Go raw string.

   For day-to-day iteration, do **not** rebuild the disk image just to change
   service startup. `examples/basic-v86-9legacy-direct.html` refreshes
   `/n/host/boot/halatha.local`, `/n/host/boot/ygg-up.rc`, and
   `/n/host/boot/yggdrasil.peers` from this directory every page load. Smoke a
   changed script inside the guest with `rc /n/host/boot/ygg-up.rc`, then inspect
   `/tmp/ygg-start.log`. The disk image should be treated as a golden base and
   rebuilt only when kernel/root contents must change.

2. **Page side — the TCP bridge (WISP relay).** The relay is *not* guest-
   startable: it lives in the browser/v86 layer. v86's default `fetch` backend
   gives the guest DHCP + HTTP but **no raw outbound TCP**, so yggdrasil runs
   yet cannot dial TCP peers. To let it, launch the page with
   `?relay=wisp://your-relay:port`, or — to automate it on every boot — set
   `WISP_RELAY` near the top of `examples/basic-v86-9legacy-direct.html` to
   your relay endpoint (per-launch `?relay=` still overrides). You must run a
   WISP server on a node that can reach your Yggdrasil peers (e.g. the wisp
   relay from v86/copied.sh). With WISP in place, the guest's outbound TCP is
   routed WebSocket→real-TCP by v86, and yggdrasil dials peers normally.

So: `-ygg` makes yggdrasil come up on its own at boot **and auto-inject public
peers** so it joins the mesh; `WISP_RELAY` (or `?relay=wisp://…`) is what gives
that yggdrasil real outbound TCP to actually dial those peers. With `-ygg`
alone (default fetch backend) you get a running node with an overlay address
and peers injected into its table, but the dials can't complete until WISP
gives it a TCP path. Under QEMU user networking the peers dial for real
(no WISP needed) — that's how the build-time verify proves the mesh join.

#### objective-9c toolchain, prebuilt and installed by default

`mk9legacyimg -o9 <path>` (default `../objective-9c`) bundles the **prebuilt**
objective-9c toolchain into the image at the paths `mk install` would use, so a
booted 9legacy guest has `o9c` ready to use and develop with — no in-guest build.
This is the distribution avenue for the language: boot the VM, write `.o9`, run
`o9build`.

The prebuilt is read from `<o9dir>/release/386/` — the language's release
artifact (a 386 Plan 9 build, a consistent `o9c`+`libo9.a` pair). Keeping it
there means the prebuilt lives in the language repo (the thing being
distributed); halatha only installs it. To refresh it, rebuild on a
9legacy/9front guest (`mk install` to a prefix) and drop the result into
`release/386/`:

```
release/386/
  bin/o9c            -> /386/bin/o9c            (0775)
  lib/libo9.a        -> /386/lib/libo9.a        (0664)
  include/o9.h       -> /sys/include/o9.h       (0664)
  rc/o9build         -> /rc/bin/o9build         (0775)
  rc/o9proj          -> /rc/bin/o9proj          (0775)
  rc/o9plumb         -> /rc/bin/o9plumb         (0775)
  rc/o9plumbbuild    -> /rc/bin/o9plumbbuild    (0775)
  src/o9_dispatch.s  -> /sys/src/cmd/o9_dispatch.s   (0664)
  src/o9_runtime.c   -> /sys/src/cmd/o9_runtime.c    (0664)
  src/o9_tab_discard.c -> /sys/src/cmd/o9_tab_discard.c (0664)
  stdlib/*.o9        -> /sys/lib/o9/stdlib/*.o9 (0664)
```

The image already ships the C toolchain (`8c`/`8l`/`8a`, `yacc`, `ar`, `mk` in
`/386/bin`), so `o9build` can compile the generated C and link `libo9.a`
in-guest — only the o9-specific pieces need bundling. The toolchain targets
**both 9front and 9legacy from one compiler**: the facade codegen in
`o9c/grammar.d/50-app-facade.y` wraps its `srvrelease`/`srvacquire` compat shim
in `#ifndef O9_HAVE_SRVRELEASE`, and `o9build` probes the local `<9p.h>` at
facade-build time — defining `O9_HAVE_SRVRELEASE` when the real lib9p API is
visible (9front: requests interleave via the real `srvrelease`/`srvacquire`)
and leaving it undefined otherwise (9legacy: the no-op shim is compiled in,
since 9legacy's lib9p does not expose those symbols). The same `o9c`+`o9build`
therefore works on both; nothing about the binary is 9legacy-specific. (The
prebuilt in `release/386` must be rebuilt in-guest — `mk o9c` then copy `o9c`
into `release/386/bin/` — to emit the guarded shim; an older prebuilt predating
this change emits the shim unconditionally and soft-regresses 9front.)

`-o9` is on by default (default path `../objective-9c`); the build skips it with
a warning if that directory is absent, and errors if the directory exists but
`release/386/` is missing (the prebuilt is required when the language repo is
present). Pass `-o9 ''` to skip explicitly. Idempotent: re-runs overwrite in
place. The bundled source tree at `/usr/glenda/src/objective-9c/` and the
`/usr/glenda/o9legacy-smoke.rc` rebuild+verify harness remain in the base image
for anyone who wants to rebuild the toolchain in-guest.
