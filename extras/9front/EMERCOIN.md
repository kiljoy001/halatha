# `.coin` names through Emercoin 9P

This is the Plan 9 side of the global-grid name layer:

```
Emercoin NVS (dns:<name>)
        │  RPC (name_show)
   emercoin9p (user-space 9P server)
        │  9P /ctl /data /status
   /n/emercoin
        │
   ndb/cs coinlookup
        │  dial / 9pmount
   Yggdrasil AAAA + IL 9P
```

The kernel never reads a blockchain or a `.coin` name. It only sees the
9P mount and the normal `cs` dialect.

## The NVS record

`name_show dns:halatha.rentonsoftworks.coin` returns, through the
`emercoin9p` `ctl`/`data` files:

```json
{"name":"dns:halatha.rentonsoftworks.coin",
 "value":"AAAA=201:dd03:96a:1c97:7b9a:98ee:c9ab:1e60",
 "txid":"86c4282c2ef0af7edc703d00dce26287a4af3650ddfe08707f315996af88a8cd",
 "address":"em1qxs6u47t2v6ml5azu63akvp9a3jn35kaysevt59",
 "vout":0,"expires_in":1910321,"expires_at":2728001,"time":1789829623}
```

The `value` is a `;`/newline-separated set of records. The current
resolver understands `A=`, `AAAA=`, `TXT=` and `SRV=`; address records
become `ip` tuples for `cs`, and text/service records are retained in the
tuple for callers that want them.

## The cs lookup

The 9front `cs` source has a `coinlookup` that is tried before DNS for a
host ending in `.coin`:

- write `name_show dns:<host>` to `/n/emercoin/ctl`
- read the JSON from `/n/emercoin/data`
- copy the `value` field and split the `A`/`AAAA`/`TXT`/`SRV` records
- return `ip` tuples to the normal `iplookup`/`iptrans` path

If the mount is absent or the record is missing, it falls through to the
normal DNS/`ndb` path. The patch is in `il-ipv6.patch` (`sys/src/cmd/ndb/cs.c`).

The `cs` process must be able to see `/n/emercoin`. In the test the mount is
made first and `cs` is restarted; in production mount the namespace **before**
starting `ndb/cs` (or restart `cs` after the mount). The `cs` protocol itself
did not change; only the `.coin` lookup was added.

## The 9pmount + babyfs path

The same `.coin` resolver reaches real services. `babyfileserver.rentonsoftworks.coin`
has the NVS record:

```text
AAAA=200:f359:ca2b:6a10:e30c:e663:6bd7:363d
```

with DNS disabled and only the `cs` 9P lookup available:

```rc
9pmount '9p+tcp://babyfileserver.rentonsoftworks.coin!17026/' /n/baby
cat /n/baby/status
```

returned:

```text
No request submitted. Administrator approval is required.
```

That is the babyfs PKI request interface. The `.coin` name resolved through
Emercoin NVS, the dial went to the Yggdrasil address over TCP, and the 9P
request service answered. A root attach is an empty aname; the explicit `/`
aname is rejected by the request service, so `9pmount` now uses `srv -nq`
with three arguments for a root mount and only uses the separate
`srv`/`mount` form for a non-root aname.

The private export on `:564` is the authenticated side; use the normal
factotum path (`?auth=factotum`) and the enrolled client key. The request
endpoint remains unauthenticated and write-limited.

## Bootstrap

The `.coin` records point at Yggdrasil addresses, but the Emercoin node
itself has to be found first. Put its Yggdrasil address in the local
`ndb` file, for example:

```
ip=201:2bd3:1946:41ea:d21a:a129:2b2f:c3a6 emercoin
```

Then mount the 9P service before starting `cs`:

```rc
mkdir -p /n/emercoin
9pmount '9p+tcp://emercoin!5640/?auth=factotum' /n/emercoin
# or: srv -nq 'tcp!emercoin!5640' emercoin /n/emercoin
```

The current test used an SSH tunnel and `10.0.2.2:5641`; the node address in
production is its Yggdrasil address. The `emercoin9p` server only listens on
TCP today; TCP over the Yggdrasil overlay is fine, and the 9P-over-IL work
applies to the target services.

## Verification (DNS disabled)

With `/n/emercoin` mounted, `cs` restarted, and `ndb/dns` stopped so a DNS
fallback cannot mask the result:

```
tcp!halatha.rentonsoftworks.coin!443
 -> /net/tcp/clone 201:dd03:96a:1c97:7b9a:98ee:c9ab:1e60!443

il!halatha.rentonsoftworks.coin!17026
 -> /net/il/clone 201:dd03:96a:1c97:7b9a:98ee:c9ab:1e60!17026
```

`/n/emercoin/status` was `OK!` and `/n/emercoin/data` held the record above,
so the lookup went through the 9P namespace, not DNS.

## Security

`emercoin9p` exposes its `ctl` file, and that file executes arbitrary
Emercoin RPC commands. The test server was bound to `127.0.0.1` on the
Emercoin node (reached through an SSH tunnel); it was not exposed on the
Yggdrasil address.

Before this is a public grid service:

- Bind only the node's Yggdrasil address, never `0.0.0.0`.
- Enable `EMC_9FRONT_AUTH_DOMAIN`/`USER`/`PASS` (dp9ik) and mount with
  `auth=factotum`/`amount`; keep the key in secstore/factotum.
- Add a command allowlist to `emercoin9p` (`name_show`, `name_scan`,
  `getblockheader`, `getrawtransaction`, `getblockhash`) and reject wallet
  commands. The source's `CommandFile` currently forwards every RPC; this
  is the important hardening step.
- Keep the Emercoin RPC on `127.0.0.1` (`rpcallowip`) and use a dedicated
  RPC credential; do not expose the wallet RPC.
- The `cs` process runs as `none`; the mount's `ctl`/`data` permissions must
  allow only the read-only query it needs.

The `name_show` result carries `txid`/`vout`; a trustless client can verify
the name against the block Merkle root (the `emercoin9p` repository already
contains `merkle.go`; `stepca-9p` has a 9P client for the same namespace).
The gateway is then a convenience, not the trust anchor.

## Global grid shape

- **Names**: Emercoin NVS `dns:` records with Yggdrasil `AAAA`/`A` and
  `TXT`/`SRV` service metadata.
- **Resolution**: `emercoin9p` 9P namespace + `cs`/`ndb`; `9pmount` for
  `9p+il://name.coin!port/path` once `cs` can resolve the name.
- **Transport**: Yggdrasil for the overlay; IL for 9P services that support
  it; TCP over Yggdrasil for services that do not.
- **Authentication**: secstore/factotum + dp9ik for the 9P services; the
  Yggdrasil source address remains the network identity.
- **Local state**: `ndb` holds the bootstrap node and static/cached names;
  the `cs` plugin provides the live NVS path.
- **Trust**: blockchain-anchored NVS records with Merkle proofs; the 9P
  server is a gateway, not an authority.

The `babyfs` PKI gate is independent of this naming layer: a `.coin` record
can point at the gate's Yggdrasil address, and the normal `Tauth`/Ed25519
path still runs over the selected transport (IL included).

## Allowlist (implemented)

`emercoin9p` now defaults `NewNsFromEnv` to a read-only command set:

```text
name_show,name_scan,getblockheader,getrawtransaction,getblockhash,getblock
```

`EMC_ALLOW_COMMANDS` overrides the list; `all` or `*` restores the old
unrestricted behavior. `getbalance`, `sendtoaddress`, `getinfo` and the
other wallet commands are rejected before the RPC backend. The test server
on the Emercoin node is the `emercoin9p-allow` binary at
`127.0.0.1:5640`, reached through an SSH tunnel; it was never bound to the
Yggdrasil address. `cs` truncates `/n/emercoin/ctl` with `OTRUNC` before
each query, so a shorter command cannot inherit the tail of the previous
one.

The allowlist commit is `3b30dc3` on `kiljoy001/emercoin-9p`
(`feature/docker-k8s`). The 9front pieces are `215d60e5a` (coin lookup) and
`35435fbed` (ctl truncation), with `66910429e` for the IL changes.

## Test service

The node `rentonsoftworks.coin` runs `emercoind` with RPC on
`127.0.0.1:6662`; `emercoin9p-allow` serves the 9P namespace on
`127.0.0.1:5640`. The Plan 9 test reaches it through an SSH tunnel as
`10.0.2.2:5641`. In production, bind the node's Yggdrasil address, enable
`EMC_9FRONT_AUTH_*` (dp9ik), and mount with factotum/secstore:

```rc
9pmount '9p+tcp://emercoin!5640/?auth=factotum' /n/emercoin
```

then start/restart `cs` so it inherits the mount. The `.coin` records then
resolve through `cs`; target services use `9pmount` with `il` or `tcp`.

## The grid, end to end

- **Naming**: Emercoin NVS `dns:` records (`AAAA=`, `A=`, `TXT=`, `SRV=`)
  hold Yggdrasil addresses and service metadata; `halatha` is
  `201:dd03:...`, `babyfileserver` is `200:f359:...`.
- **Bootstrap**: `ndb` has the Emercoin node's Yggdrasil address; the 9P
  namespace is mounted before `cs` or `cs` is restarted.
- **Resolution**: the `cs` `coinlookup` queries `/n/emercoin` and returns
  `ip` tuples; DNS is only a fallback. With DNS disabled, both
  `tcp!halatha...!443` and `il!halatha...!17026` resolve to
  `201:dd03:...`.
- **Transport**: the record's Yggdrasil IPv6 address is dialed directly;
  IL is used when the target 9P service supports it, otherwise TCP over
  Yggdrasil.
- **Authentication**: secstore/factotum + dp9ik for the `emercoin9p` mount
  and for target 9P services; the babyfs PKI keeps its Tauth/Ed25519 and
  Yggdrasil-source binding.
- **Trust**: the NVS reply carries `txid`/`vout`; the `merkle.go` library
  and `stepca-9p` client can verify the record against the block Merkle
  root. The gateway is a convenience, not the trust anchor.
- **Local state**: `ndb` is the bootstrap/cache; `cs` is the live resolver;
  `secstore` holds the keys; `9pmount` is the user-space URI/mount helper.

The live web UI still uses `halatha.rentonsoftworks.com` over the
Yggdrasil address; `.coin` lacks a matching Caddy certificate. The `.coin`
name is the naming/9P layer, not a certificate name.

## Images

`local/9front-halatha-il-cs.img` is the 9front image with the improved IL,
the `cs` `.coin` lookup and `9pmount` with the root-aname fix. The earlier
`-cs-v1`/`-cs-v2` images are development snapshots. The live page can boot
the new image with `?iso=/local/9front-halatha-il-cs.img&size=2147483648`;
the `/n/emercoin` mount and the production node address still need to be
added to the boot hook before `.coin` resolution is live in the browser.
