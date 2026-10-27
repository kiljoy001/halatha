# TLS 1.3 transport for 9front

`tls13proxy` is a user-space TLS 1.3 client. It accepts a TCP connection on
9front loopback and forwards its bytes to one TLS 1.3 server. The remote
certificate and hostname are verified by Go's `crypto/tls` and `crypto/x509`.
There is no option to skip verification or negotiate an older TLS version.

This is a first transport for o9c applications while 9front's `libsec` and
`/net/tls` still implement TLS 1.2. It does not replace those interfaces or
provide an HTTPS parser. An o9c program can use its existing `NetConn` class
to dial `tcp!127.0.0.1!14443` and then write HTTP/1.1 requests through the
connection.

Build on a Go host:

```
GOOS=plan9 GOARCH=amd64 CGO_ENABLED=0 go build -o tls13proxy.amd64 ./extras/9front/tls13proxy
```

Run on 9front after configuring loopback:

```
ip/ipconfig loopback /dev/null 127.1 /128
tls13proxy -listen 127.0.0.1:14443 -target manager.example:1517 -servername manager.example -ca /sys/lib/tls/ca.pem
```

Without `-ca`, Go uses the system trust store. `-ca` selects an explicit PEM
trust store. The listener only accepts numeric loopback addresses. Keep
application credentials in the application; the proxy only handles TLS.
On Plan 9, Go looks for the system trust store at `/sys/lib/tls/ca.pem`.
For servers requiring a client certificate, pass `-cert client.crt -key client.key`
together. Both files must be readable by the process; protect the private key.

Run the isolated 386 VM test from the repository root:

```
python3 dev/test-tls13-qemu.py /path/to/il-enabled-9front.img local/tls13-vm-test
python3 dev/test-tls13-pair-qemu.py /path/to/il-enabled-9front.img local/tls13-vm-pair
```

The test copies the disk, builds the Plan 9 binary, creates test certificates,
and checks verified TLS 1.3, a 256 KiB response, wrong CA, wrong hostname,
rejection of a TLS 1.2-only server, and mutual TLS. The base image is never
modified.
The second test checks a TLS 1.3 connection between two 9front VMs on an
isolated QEMU socket network.
