# Selective app porting

The guest image is not a 9front install. Every command is a source port
against Harvey's headers, libc and kernel, built on the host with clang and
linked statically. Copying a 9front binary is not a port: the kernel, libc,
devices and authentication plumbing are different.

This file is the porting ledger. Add one row per app, and keep the status
honest: "builds" is not "ported".

## Scope

In scope, in order:

1. networking core and clients
2. authentication
3. small operational tools the above need

Out of scope: Plan 9 compilers (`6a`/`6c`/`6l`) and `/sys/src`, editors and
office apps, mail, disk/fs servers, games, audio, and hardware-specific
`aux`/`reform`/`bitsy` tails.

Target priority:

1. virtual machines (test and CI)
2. the SBC (hardware target, ARM64)
3. x86 hardware last; not a near-term priority

The in-guest build story is a separate workstream: musl plus the APEX layer,
then a plan9port updated for musl, so old Plan 9 C can be compiled on the
target. We do not port the old Plan 9 compilers.

## Runtime classes

Two runtimes coexist, and a port must say which one it targets:

| Class | Libc / ABI | Uses | Notes |
|---|---|---|---|
| native | Harvey libc, static ELF | system commands, `/net`, Rio helpers | current image |
| posix | musl + APEX | ported Unix/plan9port apps | in progress elsewhere |
| plan9port | musl + APEX + plan9port libs | old Plan 9 C that uses the Plan 9 API | port libraries and selected commands only |
| o9 | generated Plan 9 C + Harvey libc | o9 apps | can also target the posix class later |

A native port must not depend on POSIX sockets; a posix port must not depend
on Harvey libc internals. The file interfaces (`/net`, `/n/host`, 9P) are the
contract between them.

The compiler work is owned elsewhere. App porting needs only a stable
interface from it: target triple and ABI, ELF object format, sysroot and
headers, linker, startup object, and a hello-world plus a Plan 9 C test built
on the target.

### APEX constraints

The original APEX design is documented in its wiki; these are the parts that
matter for porting:

- APEX is musl plus wrappers over the **native syscall API**. It is not a
  Linux syscall personality and it does not duplicate the kernel interface.
- One compiler and one ABI for native and posix code: Harvey ELF, register
  syscalls. `libap` is the only extra library an ANSI/POSIX program needs.
- Static linking for v1. No dynamic loader, no `dlopen`; do not assume
  threads until the APEX layer proves them.
- Do not mix native `u.h`/`libc.h` and POSIX headers in one source file.
  Separate translation units, as the APEX wiki recommends.
- plan9port is a source port of the Plan 9 libraries, not a second kernel.
  Port `lib9`, `libutf`, `libfmt`, `libbio`, `lib9p` and the commands that
  need them. Do not link native `libc`/`libbio` into a plan9port binary;
  symbol collisions (`print`, `fmt`, `Bio`) are real.

The old APEX wiki also states the honest limitation: "for now we are
compiling in Linux and running in Harvey." In-guest building is the new part,
and it should not be a precondition for porting apps.

## Port unit

A port is not done until all of these exist:

- source and exact revision (Harvey tree or 9front file)
- dependency closure (libs, `/lib/ndb`, `/dev` entries, kernel devices)
- patches carried by the build (and why)
- guest test that exercises the real behaviour
- size and boot-time impact
- deployment: baked into the GEFS image or hot-loaded from `/n/host`

Keep patches small and local. Prefer a compat header or a small source shim
over editing a 9front file; when a source change is unavoidable, record it in
the ledger and in `build_aspera_tools.py`.

## Dependency order

```
libip  ──> ipconfig, ping
libndb ─> cs, dns, csquery, dnsquery      (libndb.a already builds)
libip + libndb ──> dial
dial + lib9p + libbio ──> webfs           (9front-only source)
libsec ─> tlsclient                       (9front-only source)
libauth + libsec ──> factotum, keyfs      (9front-only source)
factotum + libsec ──> ssh, sshfs, sshnet  (9front-only source)
```

Networking is not usable until the guest also has `/net` mounted, an ndb
configuration, and a packet backend. The backend is platform-specific:
virtio-net under a VM, gmac/wifi on the SBC, Wisp in the browser. Those are
tracked separately from the binary ports. Apps in the posix class go through
APEX sockets and the same underlay.

## Ledger

Each row must declare its runtime class (native/posix/o9) and the surfaces it
needs: libc-only, `/net`, 9P, draw, auth. That is what decides whether it can
be a native build, needs APEX, or needs a namespace/9P bridge.

| App | Source | Depends on | Status | Notes |
|---|---|---|---|---|
| `libndb` | `sys/src/libndb` | libc | built | `build/libndb.a` |
| `libip` | `sys/src/libip/libip.json` | libc | built | `build/libip.a`; `util/9vm/build_libip.py` |
| `cs` | `sys/src/cmd/ndb/cs.c` | libndb, libip, libauth, lib9p | built | `build/aspera-cs`; runtime needs `/net` |
| `dns` | `sys/src/cmd/ndb/dns.json` | libndb, libip, libsec | built | `build/aspera-dns`; runtime needs `/net` + `/lib/ndb` |
| `csquery` | `sys/src/cmd/ndb/csquery.c` | libndb | built | `build/aspera-csquery` |
| `dnsquery` | `sys/src/cmd/ndb/dnsquery.c` | libndb, libip | built | `build/aspera-dnsquery` |
| `ipquery` | `sys/src/cmd/ndb/ipquery.c` | libndb, libip | built | `build/aspera-ipquery` |
| `dial` | `sys/src/libc/9sys/dial.c` | libip, libndb | built | in libc; used by the guest tcp test |
| `ipconfig` | `sys/src/cmd/ip/ipconfig/ipconfig.json` | libip, libndb | built | `build/aspera-ipconfig`; runtime needs the packet link |
| `ping` | `sys/src/cmd/ip/ping.c` | libip | built | `build/aspera-ping`; first end-to-end link test |
| `webfs` | 9front `sys/src/cmd/webfs` | lib9p, libbio, libsec | later | not in the Harvey tree; HTTP/HTTPS client filesystem |
| `tlsclient` | 9front `sys/src/cmd/tlsclient.c` | libsec | later | TLS for webfs and auth |
| `factotum` | 9front `sys/src/cmd/auth/factotum` | libauth, libsec | later | keys and TLS/SSH credentials |
| `keyfs` | 9front `sys/src/cmd/auth/keyfs.c` | libauth, libsec | later | key database |
| `ssh` | 9front `sys/src/cmd/ssh.c` | libauth, libsec, factotum | later | only if internal systems use SSH |
| `sshfs`/`sshnet` | 9front `sys/src/cmd/sshfs.c`, `sshnet.c` | libauth, libsec | later | optional |
| `exportfs` | Harvey/9front `sys/src/cmd/exportfs` | lib9p, libauth | later | only if exporting namespaces |

`lib/ndb/local` is staged in `local/aspera/rootfs/lib/ndb/local` and copied by
`build_aspera.py`. The net binaries are in the GEFS image. `vps_init` binds
`#I` and `#l` under `/net`, then runs `/lib/aspera/net.rc`, which does DHCP,
starts the resolver and the connection server.

The browser link uses v86's NE2000 (`netdev="ne2k"`, Realtek 8029) and a
tokenless Wisp TCP relay.  v86's i440fx virtio-net publishes all PCI
capability offsets as zero, so a modern virtio driver cannot locate its
common/notify/ISR registers; the ported `ether8390`/`ether2000` driver is the
supported path.  DHCP, DNS (`cs` -> `dns` -> v86 DoH), ARP and ICMP were
verified in the browser; 9vm uses virtio and the same kernel/image boots the
real VPS path locally via `util/9vm/9vm -initrd build/aspera-vps.gefs ...`.

## Known Harvey/9front deltas

- `libndb.a`, `libauth.a`, `libsec.a`, `lib9p.a`, `libbio.a`, `libthread.a`
  already build.
- `libbio` gained `Blethal`/`Biofn`/`Berror` and the `errorf`/`iof`/`aux`
  fields for the 9front tools. Keep that as the standard bio surface.
- 9front's newer timezone API breaks `date`; port it separately or keep it
  out.
- The `page` event API is missing; anything depending on it (`page`, some
  graphics apps) is not a port candidate yet.
- `sed` needs a source patch for clang's handling of untagged anonymous
  unions; already carried.
- `webfs`, `ssh`, `tlsclient`, `factotum` and `keyfs` live in the 9front
  tree, not Harvey's; they are ports, not builds.
- No `.u`: Wanix and `dev9p` are plain 9P2000.

## First batch

The pilot is the smallest path to a real `ping`:

1. `build_libip.py` → `libip.a` — done
2. `ipconfig` (DHCP and static) — built
3. `cs` — built
4. `dns` + minimal `/lib/ndb/local` — built/staged
5. `ping` — built
6. `dial` — port from 9front

Then, only after the packet backend exists: `webfs` + `tlsclient`, then
`factotum` + `ssh`.

### POSIX stack gates

Do not port a posix-class app until these pass, in order:

1. musl `hello` with stdio, static, on the VM
2. plan9port `lib9` plus one command (`basename` or `ls`) on the VM
3. POSIX file, process and socket smoke tests; sockets reach native `/net`
4. repeat 1–3 on the SBC/ARM64
5. build 1–3 in-guest with the new compiler

A gate is a run, not a link. Each one removes a class of doubt before the next
app is attempted.

## Compiler plan: tcc

tcc is the intended in-guest compiler: a small hosted C compiler with an
integrated assembler and linker, emitting ELF64. It is not a Plan 9 compiler
port; the Plan 9 dialect support is a bounded set of extensions.

One compiler binary can serve both runtime classes, because both use the same
Harvey ABI and ELF format. The class is selected by the include/library set:

- **native**: `-I amd64/include -I sys/include`, link `build/libc.a` (plus
  `libbio`, `lib9p`, ...), entry point `main`, static; Plan 9 C API.
- **posix**: APEX/musl headers and `libap`, same ABI/ELF; POSIX C.

tcc itself should run in the posix class. It uses stdio and POSIX file I/O,
which native Harvey libc does not provide. The compiler's own libc does not
determine the output class; the sysroot does.

Requirements to prove early:

- ELF64, x86-64 SysV/register ABI, static. Harvey's kernel sets up argc/argv
  and the current binaries use `-e_main`; tcc output must enter at `main` or
  provide an equivalent startup object.
- **Harvey ELF layout: 2 MB-aligned `PT_LOAD` segments** (`p_align` and
  `p_vaddr` at 0x200000 boundaries, base 0x200000). tcc's default 4 KB layout
  loads but faults on the first instruction; the kernel's `elf64ldseg` only
  warns when `p_align < BIGPGSZ`. Check with
  `readelf -l` against a working clang-built binary.
- `__builtin_va_list` / `va_start` / `va_arg`, `__builtin_return_address`,
  `_Alignas`, `_Static_assert`.
- anonymous struct/union members, and `-fplan9-extensions` tagged anonymous
  members.
- K&R definitions and implicit int in old sources.
- link against clang-built `libc.a`: standard ELF relocations, no TLS, no
  dynamic linking, no constructors.
- a `libtcc1.a` built for Harvey (64-bit helpers, float conversions).
- C only. Plan 9 `.s` files stay with clang; tcc does not need the Plan 9
  assembler syntax.

Dialect gate tests, in order:

1. `u.h` + `libc.h` + `print` hello, native libc, VM.
2. `va_list` formatting (`snprint`) and `libbio` (`ls`).
3. tagged anonymous member plus bitfields.
4. tcc compiles itself under APEX/musl.
5. arm64 output for the SBC, or keep host cross-build until tcc's arm64
   backend is proven.

If tcc cannot handle a plan9port source that uses GCC extensions
(`typeof`, statement expressions, `__attribute__`), use clang for that class
instead of weakening the split.

## Reuse across Aspera platforms

The browser is one platform, not the product. The reusable layer is the guest
userland and its file interfaces. A platform supplies devices and services
through 9P; apps do not learn a platform API.

| Layer | Reused across platforms | Platform-specific |
|---|---|---|
| o9 runtime and apps | yes | — |
| libc, lib9p, libauth, libsec | yes | — |
| `/net`, `ipconfig`, `cs`, `dns`, `dial` | yes | link driver |
| `webfs`, `tlsclient`, `factotum`, `ssh` | yes | key storage, link |
| Rio, console, fonts | yes | framebuffer, keyboard, mouse |
| storage | file API | block device / image / browser FSA |
| host services | `/n/host` file API | FSA, WebUSB, fetch vs native drivers |
| packet backend | — | Wisp (browser) vs ethernet/wifi |
| boot and image transport | — | multiboot module, disk, firmware |

Rules:

1. Apps see files, not platform APIs. Ported apps must not call browser
   JavaScript; services are 9P.
2. Networking stays in the guest. Wisp is a link-layer shim, not the network
   stack; `ipconfig`, `cs`, `dns` and `dial` are the same on every platform.
3. Auth stays in the guest. A platform supplies key storage and a link; it
   does not own policy.
4. The toolchain is a platform asset, not an app concern. Host cross-build
   remains the release path for every target. In-guest builds use the
   musl/APEX/plan9port workstream, not `6a`/`6c`/`6l`. App sources must not
   depend on which of the two built them.
5. Build descriptors separate the target (arch, ABI, libs) from the platform
   (drivers, init, services), so an app port is built once and linked for
   each target.
6. Tests exercise the guest, not the page. The browser harness is one runner;
   the same guest-side tests should run under 9vm, QEMU and real hardware.

The current browser page and `host/` services are allowed to be
platform-specific. The app ports and the `/net` stack must not depend on them.

## Platform matrix

| Platform | Priority | Boot | Packet link | Console | Host services |
|---|---|---|---|---|---|
| VM / 9vm / QEMU | 1 (test) | multiboot or disk | virtio-net / e1000 | serial / VGA | 9P host tree |
| SBC (ARM64) | 2 (hardware) | firmware | gmac / wifi | native display | native 9P services |
| browser / v86 | test only | multiboot + GEFS module | virtio-net + Wisp | v86 VGA + Rio | browser `host/` ramfs |
| x86 hardware | last | firmware | ethernet | native VGA / serial | native 9P services |

Fill in the last rows before porting an app that depends on a device class.

## Selection rule

Pick the next app only when:

- an internal system actually speaks its protocol, or the network core needs
  it;
- its dependency closure is available (or the missing dependency is the next
  port);
- there is a guest test that proves it, not just a successful link;
- its image/runtime cost is acceptable.

Otherwise leave it out. The image stays curated; optional apps are
hot-loaded from `/n/host`.
