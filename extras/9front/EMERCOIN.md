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
