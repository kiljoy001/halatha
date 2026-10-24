# 9pmount

`9pmount` is the user-space resolver for the `9p://` locator grammar. The
kernel never parses a URI: this tool turns one into a Plan 9 dial string (or
a kernel channel) and drives `srv(1)`/`mount(1)`.

```
9p://host!port/aname        tcp, default port 564
9p+tcp://host!port/aname    tcp
9p+il://[200::1]!564/aname  IL, including IL over the Yggdrasil pkt iface
9p+tag://host9p/aname       virtio-9p tag (#9/host9p)
9p+ring://0/aname           memory-ring channel (#~/0)
```

The path is the 9P attach name. Query parameters:

- `aname=...` overrides the path
- `auth=factotum` uses `amount` (the normal factotum path)
- `auth=none` (the default) uses `mount -n`

## Authentication and the babyfs PKI gate

The babyfs request endpoint (`:17026`) is deliberately unauthenticated: it
exposes `status` and a write-only `request` file, and an administrator
approves the submitted key. Mount it with the default:

```sh
9pmount '9p+il://[200:f359:ca2b:6a10:e30c:e663:6bd7:363d]!17026/' /n/request
cat /n/request/status
```

The private export (`:564`) is the PKI-protected side. It uses 9P `Tauth`,
an Ed25519 challenge bound to the connection's Yggdrasil source address,
and an explicit allowlist. Do **not** use `auth=none` there:

```sh
9pmount '9p+il://[200:f359:ca2b:6a10:e30c:e663:6bd7:363d]!564/data?auth=factotum' /n/private
```

IL carries the same 9P `Tauth` exchange as TCP; the source address the gate
sees is still the client's Yggdrasil address. A TCP proxy in front of babyfs
would break that binding, so the IL listener belongs on babyfs itself.

## Build

```sh
GOOS=plan9 GOARCH=386 CGO_ENABLED=0 go build -o 9pmount.386 ./cmd/9pmount
go test ./internal/ninepuri
```

`mk9frontimg.py` installs `local/9pmount.386` as `/386/bin/9pmount` when
`NINEPMOUNT_BIN` (or the default path) exists.

## `.coin` names through Emercoin

Once `/n/emercoin` is mounted from the `emercoin9p` service and `cs` has the
`coinlookup` patch, a `.coin` name is an ordinary host name:

```sh
9pmount '9p+il://halatha.rentonsoftworks.coin!17026/' /n/target
9pmount '9p+tcp://emercoin!5640/?auth=factotum' /n/emercoin
```

`cs` resolves the `.coin` name through the 9P namespace, returns the
Yggdrasil IPv6 address, and the selected transport (`il` or `tcp`) dials it.
The `/n/emercoin` mount must exist before `ndb/cs` starts, or `cs` must be
restarted after the mount. The `coinlookup` handles `A=`, `AAAA=`, `TXT=`
and `SRV=` records; see [EMERCOIN.md](../../extras/9front/EMERCOIN.md) for
the bootstrap, secstore/dp9ik and the read-only allowlist that the
`emercoin9p` `ctl` needs before it is exposed beyond the node.

## IL over Yggdrasil notes

- The kernel must have `il` and `pktmedium`. The guest image is built with
  the improved `il.c` from the 9front checkout (IPv6, credits, cwnd).
- After Yggdrasil creates its `pkt` ipifc, run `echo refresh >/net/cs` so
  `cs` learns about the new IPv6 interface. `halatha.local` already does
  this; the standalone `iltest.rc` does it explicitly.
- `cs` must be the patched binary with `V4|V6` for the `il` network. Without
  the refresh, even a numeric `tcp!<IPv6>!port` is reported as
  `can't translate address`.
- For a Yggdrasil MTU of 1500, keep 9P messages under the IL datagram size
  (for example, `exportfs -m 1400`).

A two-node test (one QEMU guest serving, one mounting, both on the public
Yggdrasil mesh) returned:

```
Established ... cwnd 17 flight 0 ssthresh 20 flow 1 sent 263 read 367 peerread 252
hello-from-remote-il
```

with the client dialing `il!200:9734:6658:71cb:d314:94c4:f907:3654!17026`.
