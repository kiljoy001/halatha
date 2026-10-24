# Adding IL to the babyfs PKI gate

babyfs is an access-control service, not a generic file server. The IL
transport must terminate **on babyfs**, not in a proxy: the gate binds the
client's Ed25519 signature to the connecting Yggdrasil source address. IL
over Yggdrasil preserves that address; a TCP proxy would replace it.

The current deployed endpoint is:

```
tcp!200:f359:ca2b:6a10:e30c:e663:6bd7:363d!17026
```

`:17026` is the unauthenticated key-request interface; the private export on
`:564` uses 9P Tauth.

## Server side (9front/amd64)

1. Build the kernel with `il` and `pktmedium` and the improved `il.c`
   (IPv4/IPv6, credit flow control, cwnd). Yggdrasil already creates the
   `pkt` ipifc; no TUN driver is needed.
2. Keep the existing gate command exactly as it is. Add an IL service entry
   beside the TCP one, for example by copying
   `/rc/bin/service/!tcp17026` to `/rc/bin/service/!il17026` (and, if the
   private export is served there, `!tcp564` to `!il564`). The generic
   `aux/listen1` scanner will offer the same service on IL.
3. For the 564 export, cap the 9P message size for the Yggdrasil MTU
   (`exportfs -m 1400` for a 1500-byte interface). Do not change the
   authentication, allowlist, source-address check, nonce or signature
   handling.
4. Verify from a 9front guest built with the patched `cs`:

```rc
ip/ipconfig
ip/ipconfig loopback /dev/null 127.1 /128
echo refresh >/net/cs
srv -nq 'il!200:f359:ca2b:6a10:e30c:e663:6bd7:363d!17026' baby /n/request
cat /n/request/status
```

Then test the private path with the normal factotum mount; the request
endpoint is the only unauthenticated one.

## Client side

- The guest image must contain the improved `il` kernel and the patched
  `cs` (`V4|V6` for `il`). `local/9front-halatha-il.img` does.
- `halatha.local` refreshes `/net/cs` after Yggdrasil comes up. Without that
  refresh, `cs` reports `can't translate address` even for a numeric IPv6
  destination.
- `/opt/halatha/www/config/babyfs-endpoint` can then be changed to:

```
il!200:f359:ca2b:6a10:e30c:e663:6bd7:363d!17026
```

Keep the TCP service and the previous endpoint file backed up. The
deployment script's endpoint validation accepts both `tcp!` and `il!`.
Rollback is restoring the one-line endpoint file; the TCP listener remains
available.

## Why not a relay on the VPS

A relay would see the VPS Yggdrasil address, not the downloaded VM's. The
gate's `transportKeys` design can separate a signing key from a forwarding
node, but that is an explicit enrollment decision, not a transparent
transport swap. Use the direct IL listener unless a new allowlist entry is
intentionally being created.

## Security properties preserved

- 9P Tauth and Ed25519 are unchanged; IL is only the carrier.
- The source address is the client's Yggdrasil address because IL runs over
  the Yggdrasil pkt interface.
- The request service remains unauthenticated and write-limited; the
  private export remains allowlist-only.
- The improved IL adds flow control and congestion control, not a new
  identity or authentication layer.
