//go:build !js && !wasm

package main

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/progrium/go-netstack/vnet"
	altws "golang.org/x/net/websocket"
	"tractor.dev/wanix/fs"
	"tractor.dev/wanix/fs/fskit"
	"tractor.dev/wanix/fs/ipfsfs"
	"tractor.dev/wanix/fs/libtabfs"
	"tractor.dev/wanix/fs/localfs"
	"tractor.dev/wanix/fs/yggfs"
	"tractor.dev/wanix/internal/ygg9pauth"

	"tractor.dev/toolkit-go/duplex/mux"
	"tractor.dev/toolkit-go/engine/cli"
	"tractor.dev/wanix/fs/p9p"
	"tractor.dev/wanix/wisp"
)

func serveCmd() *cli.Command {
	var (
		listenAddr          string
		bundle              string
		wispOn              bool
		caCerts             stringAccum
		wispPeer            string
		wispPeersFile       string
		wispEmcDomain       string
		wispEmcDNS          string
		wispDeny            string
		allow9p             bool
		allowGrid           bool
		telemetryDir        string
		ipfsAPI             string
		yggSocket           string
		libtabDir           string
		guest9P             bool
		import9pTarget      string
		import9pKeys        string
		import9pOrigin      string
		import9pSocket      string
		import9pService     string
		import9pAname       string
		import9pUser        string
		import9pPairingFile string
		import9pServerKey   string
		import9pRoot        string
	)
	cmd := &cli.Command{
		Usage: "serve [dir]",
		Short: "serve directory contents with wanix overlay",
		Run: func(ctx *cli.Context, args []string) {
			var err error
			dir := "."
			if len(args) > 0 {
				dir = args[0]
			}
			dir, err = filepath.Abs(dir)
			if err != nil {
				log.Fatal(err)
			}
			dirfs, err := localfs.New(dir)
			if err != nil {
				log.Fatal(err)
			}

			log.SetFlags(log.Ltime | log.Lmicroseconds | log.Lshortfile)

			h, p, _ := net.SplitHostPort(listenAddr)
			if h == "" {
				h = "localhost"
			}
			fmt.Printf("Serving %s files with Wanix overlay ...\n", dir)
			if bundle != "" {
				fmt.Printf("Bundle available at: http://%s:%s/?bundle=%s\n", h, p, bundle)
			} else {
				fmt.Printf("Bundle available at: http://%s:%s\n", h, p)
			}

			vn, err := vnet.New(&vnet.Configuration{
				Debug:             false,
				MTU:               1500,
				Subnet:            "192.168.127.0/24",
				GatewayIP:         "192.168.127.1",
				GatewayMacAddress: "5a:94:ef:e4:0c:dd",
				GatewayVirtualIPs: []string{},
			})
			if err != nil {
				log.Fatal(err)
			}

			http.Handle("/.well-known/", http.NotFoundHandler())
			http.Handle("/.well-known/ethernet", ethernetHandler(vn))
			http.Handle("/.well-known/export9p", export9pHandler())
			if import9pTarget != "" {
				if _, _, err := net.SplitHostPort(import9pTarget); err != nil {
					log.Fatal("invalid --import-9p-target: ", err)
				}
				listenHost, _, err := net.SplitHostPort(listenAddr)
				if err != nil || net.ParseIP(listenHost) == nil || !net.ParseIP(listenHost).IsLoopback() {
					log.Fatal("authenticated local 9P imports require --listen on an explicit loopback IP")
				}
				keys, err := loadMountKeys(import9pKeys)
				if err != nil {
					log.Fatal("--import-9p-keys: ", err)
				}
				if import9pOrigin == "" {
					import9pOrigin, err = mountOriginForListen(listenAddr)
					if err != nil {
						log.Fatal(err)
					}
				}
				cfg := import9PConfig{Target: import9pTarget, Origin: import9pOrigin, Export: "network", User: "none", Aname: import9pAname, Keys: keys}
				if import9pSocket != "" {
					if import9pUser == "" {
						log.Fatal("--import-9p-factotum-user is required when USER is unset")
					}
					if !safeFactotumService(import9pService) {
						log.Fatal("invalid factotum service selector")
					}
					cfg.Signer = factotumMountSigner{socket: import9pSocket, user: import9pUser, service: import9pService}
				}
				if import9pServerKey != "" || import9pRoot != "" {
					key, err := hex.DecodeString(import9pServerKey)
					host, _, splitErr := net.SplitHostPort(import9pTarget)
					if err != nil || splitErr != nil || ygg9pauth.Address(key) == nil || !net.ParseIP(host).Equal(ygg9pauth.Address(key)) || !filepath.IsAbs(import9pRoot) || import9pRoot == "/" || cfg.Signer == nil {
						log.Fatal("signed remote imports require a pinned Yggdrasil node address/key, dedicated export root, and factotum signer")
					}
					cfg.RemoteScope = &ygg9pauth.Scope{ServerKey: key, Root: import9pRoot, Access: "read-write"}
				}
				gateway, err := newImport9PGateway(cfg)
				if err != nil {
					log.Fatal(err)
				}
				if import9pSocket != "" {
					if err := writeMountPairingFile(import9pPairingFile, dir, gateway.pairingToken); err != nil {
						log.Fatal("--import-9p-pairing-file: ", err)
					}
					fmt.Printf("Local mount pairing code saved privately to %s\n", import9pPairingFile)
				}
				http.Handle("/.well-known/import9p", gateway)
				http.Handle("/.well-known/import9p/sign", gateway.SignHandler())
				fmt.Printf("Authenticated read-only 9P import: /.well-known/import9p -> %s\n", import9pTarget)
			}

			// Same-origin WISP relay: any page served by `wanix serve` gets a
			// WebSocket->TCP relay at /.well-known/wisp with zero per-page
			// config. The page discovers it via GET /.well-known/wanix (below)
			// and auto-points its v86 network backend at it, so the guest gets
			// real outbound TCP (for Yggdrasil/grid) without the page author
			// running a separate relay or setting WISP_RELAY.
			//
			// Airtight policy: strictly limits TCP egress to the dynamic public
			// Yggdrasil peering list (bundled or emcdns discovered) and remote
			// 9P/grid services. Disallows all loopback, RFC 1918, and homelab subnets.
			// Holds the address of the guest's private 9P listener. The
			// relay reads it at dial time, so it is set by us and never by
			// the guest -- that is the whole security property.
			guest9PTarget := new(atomic.Value)

			if wispOn {
				if wispPeersFile == "" {
					candidate := filepath.Join(dir, "extras/9front/yggdrasil.peers")
					if _, err := os.Stat(candidate); err == nil {
						wispPeersFile = candidate
					}
				}
				auth := buildCertAuth(caCerts)
				wispLogger := log.New(os.Stderr, "wisp: ", log.LstdFlags|log.Lmsgprefix)
				deny, err := parseDenyList(wispDeny)
				if err != nil {
					log.Fatal(err)
				}
				dialer := buildWispSafeDialer(wispPeersFile, wispPeer, wispEmcDomain, wispEmcDNS, allow9p, allowGrid, guest9PTarget, wispLogger, deny)
				relay := &wisp.Relay{
					Logger:      wispLogger,
					Auth:        auth,
					Dial:        dialer,
					CheckOrigin: checkOriginAllowed,
				}
				http.Handle("/.well-known/wisp", relay)
				http.Handle("/.well-known/wisp/auth", wisp.CertAuthHandler(auth))
				http.Handle("/.well-known/wanix", wanixConfigHandler(auth))
				if auth != nil && auth.Enabled() {
					fmt.Printf("WISP relay at: wisp://%s:%s/.well-known/wisp (auth: cert, %d root(s); TCP inert without a valid cert)\n", h, p, len(auth.Roots))
				} else {
					fmt.Printf("WISP relay at: wisp://%s:%s/.well-known/wisp (dynamic Yggdrasil peers + 9P:564 + grid)\n", h, p)
				}
			}

			if telemetryDir != "" {
				telemetryLogger := log.New(os.Stderr, "telemetry: ", log.LstdFlags|log.Lmsgprefix)
				http.Handle("/.well-known/telemetry", telemetryHandler(telemetryDir, telemetryLogger))
				http.Handle("/api/telemetry", telemetryHandler(telemetryDir, telemetryLogger))
				fmt.Printf("Telemetry sensor collector enabled: %s\n", telemetryDir)
			}

			// Export IPFS as part of the 9P namespace rather than exposing
			// the Kubo API to the browser. The API can add, pin and
			// reconfigure the node, so it stays on localhost; the guest only
			// ever sees a filesystem. Mounting it here means the browser
			// import at /n/host reaches it with no extra plumbing.
			exported := fs.FS(dirfs)
			mounts := fskit.MapFS{}

			if ipfsAPI != "" {
				ifs := ipfsfs.New(ipfsAPI, nil)
				// Probe once so a misconfigured or down node is a clear
				// startup message instead of an opaque error on first use.
				if _, err := ifs.StatContext(context.Background(), "."); err != nil {
					fmt.Printf("IPFS: %s unreachable (%v); not exporting ipfs/\n", ipfsAPI, err)
				} else {
					mounts["ipfs"] = ifs
					fmt.Printf("IPFS exported at ipfs/ (Kubo API %s)\n", ipfsAPI)
				}
			}

			// Export the HOST's Yggdrasil daemon as a read-only filesystem.
			// The guest's own 386 yggdrasil has been unreliable, and the
			// guest cannot reach the host's daemon over the network: the
			// WISP dialer deliberately blocks loopback, RFC 1918 and the
			// VPS's own Yggdrasil address as an SSRF defense. Reading the
			// admin socket here and publishing the result into the existing
			// 9P export gives the guest a mesh view with no new egress
			// path, no new listener, and nothing to configure guest-side.
			// Read-only by construction: yggfs issues no mutating admin
			// requests, so an untrusted page cannot repeer the host node.
			if yggSocket != "" {
				yfs := yggfs.New(yggSocket)
				// StatContext is answered without touching the daemon (so
				// that `ls` stays cheap), so probe with a real read.
				probeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				_, err := yfs.OpenContext(probeCtx, "address")
				cancel()
				if err != nil {
					fmt.Printf("Yggdrasil: %s unreachable (%v); not exporting ygg/\n", yggSocket, err)
				} else {
					mounts["ygg"] = yfs
					if f, err := yfs.Open("address"); err == nil {
						b, _ := io.ReadAll(f)
						f.Close()
						fmt.Printf("Yggdrasil exported at ygg/ (host node %s)\n", strings.TrimSpace(string(b)))
					} else {
						fmt.Printf("Yggdrasil exported at ygg/ (admin %s)\n", yggSocket)
					}
				}
			}

			// Serve the attested 9legacy binary tree so the guest can mount
			// and execute from it directly. The browser used to fetch all
			// ~800 binaries over HTTP one at a time (~0.5s each, serial),
			// which could not finish inside any sane timeout, so /n/host/bin
			// held only a handful. Over 9P the guest demand-pages exactly
			// what it runs.
			//
			// Every file is checked against the Ed25519-signed manifest
			// before its bytes are returned, so a binary that fails
			// verification cannot be opened -- and therefore cannot be
			// executed -- rather than merely being flagged.
			if libtabDir != "" {
				binDir := filepath.Join(libtabDir, "bin")
				lfs, err := libtabfs.NewFromFiles(
					os.DirFS(binDir),
					filepath.Join(libtabDir, "bin.tab"),
					filepath.Join(libtabDir, "halatha-root.pub.hex"),
				)
				if err != nil {
					// Refuse to serve an unverifiable tree at all. Falling
					// back to serving it unchecked would defeat the point.
					fmt.Printf("libtab: %s NOT exported (%v)\n", binDir, err)
				} else {
					mounts["bin"] = lfs
					fmt.Printf("libtab exported at bin/ (%d attested binaries from %s)\n",
						lfs.Count(), binDir)
				}
			}

			if len(mounts) > 0 {
				exported = fskit.UnionFS{mounts, dirfs}
			}

			// The browser's import gets the served directory with the host
			// services unioned over it: a page legitimately needs both, and
			// it is same-origin with the files anyway.

			// The GUEST gets a different namespace: the services alone,
			// with no view of the served directory.
			//
			// This is the Plan 9 answer to "narrow the export" -- not a
			// path filter over one tree, but a namespace built to contain
			// exactly what the guest should see. The guest needs bin/,
			// ipfs/ and ygg/; it has no business reading index.html, the
			// examples, or the disk images, and sharing the browser's
			// namespace handed it all of them.
			//
			// Composing a second fs.FS is the whole mechanism: there is no
			// traversal to get wrong, because paths outside `mounts` do not
			// exist in this namespace rather than being denied.
			guestFS := fs.FS(mounts)

			// Give the guest a plain-9P TCP mount of this same namespace,
			// reachable ONLY through the WISP relay.
			//
			// The guest has no route to the host: the WISP dialer refuses
			// loopback, RFC 1918 and the host's own mesh address, which is
			// what stops a page using the guest as an SSRF proxy. Relaxing
			// that ban to let the guest dial the host would reopen exactly
			// that hole, because the destination would again be chosen by
			// the guest.
			//
			// Instead: bind a listener on an ephemeral loopback port and
			// teach the relay one fixed destination. The guest never names
			// an address -- it asks for "host9p" and the relay substitutes
			// the address it chose itself. There is no input to forge, and
			// the reachable surface is exactly this one 9P service, which
			// the guest is already served over WebSocket anyway.
			if guest9P {
				ln, err := net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					fmt.Printf("guest 9P: cannot listen (%v); not enabled\n", err)
				} else {
					guest9PTarget.Store(ln.Addr().String())
					go serve9P(guestFS, ln)
					fmt.Printf("Guest 9P mount at %s (guest dials %s through the WISP relay)\n", ln.Addr().String(), guest9PAlias)
				}
			}

			http.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if websocket.IsWebSocketUpgrade(r) {
					p9Handler(exported, w, r)
					return
				}

				w.Header().Add("Cross-Origin-Opener-Policy", "same-origin")
				w.Header().Add("Cross-Origin-Embedder-Policy", "require-corp")
				w.Header().Add("Access-Control-Allow-Origin", "*")

				http.FileServerFS(dirfs).ServeHTTP(w, r)
			}))
			http.ListenAndServe(listenAddr, nil)
		},
	}
	cmd.Flags().StringVar(&listenAddr, "listen", ":7654", "addr to serve on")
	cmd.Flags().StringVar(&bundle, "bundle", "", "default bundle to use")
	cmd.Flags().StringVar(&telemetryDir, "telemetry-dir", "/var/log/halatha", "directory for session telemetry and audit logs")
	cmd.Flags().StringVar(&ipfsAPI, "ipfs-api", "", "Kubo HTTP API address to export as ipfs/ over 9P (e.g. http://127.0.0.1:5001); empty disables")
	cmd.Flags().StringVar(&yggSocket, "ygg-admin", "", "Yggdrasil admin socket to export read-only as ygg/ over 9P (e.g. /run/yggdrasil/yggdrasil.sock); empty disables")
	cmd.Flags().StringVar(&libtabDir, "libtab-dir", "", "directory holding bin/, bin.tab and halatha-root.pub.hex; exports the attested binary tree as bin/ over 9P (e.g. extras/9legacy); empty disables")
	cmd.Flags().StringVar(&import9pTarget, "import-9p-target", "", "one TCP 9P file server to mount through the browser (host:port); empty disables")
	cmd.Flags().StringVar(&import9pKeys, "import-9p-keys", "", "approved Ed25519 public-key file (one hex key per line); required for imports")
	cmd.Flags().StringVar(&import9pOrigin, "import-9p-origin", "", "exact browser origin allowed to mount; defaults to the loopback listen origin")
	cmd.Flags().StringVar(&import9pSocket, "import-9p-factotum", "", "local factotum Unix socket for restricted challenge signing; empty disables signing helper")
	cmd.Flags().StringVar(&import9pUser, "import-9p-factotum-user", os.Getenv("USER"), "local factotum attach user; defaults to USER")
	cmd.Flags().StringVar(&import9pPairingFile, "import-9p-pairing-file", "", "private pairing-code output file outside the served directory; required for the signing helper")
	cmd.Flags().StringVar(&import9pService, "import-9p-service", "halatha-mount", "factotum Ed25519 key service attribute")
	cmd.Flags().StringVar(&import9pAname, "import-9p-aname", "", "fixed upstream 9P attach name")
	cmd.Flags().StringVar(&import9pServerKey, "import-9p-server-key", "", "pinned remote Yggdrasil public key; enables server-side signed Tauth")
	cmd.Flags().StringVar(&import9pRoot, "import-9p-root", "", "exact remote private export root for signed Tauth")
	cmd.Flags().BoolVar(&guest9P, "guest-9p", false, "bind a loopback 9P listener the guest reaches by dialing the fixed reserved address "+guest9PAlias+" through the WISP relay; the relay substitutes the real address, so this does not widen its egress policy")
	cmd.Flags().BoolVar(&wispOn, "wisp", true, "host a same-origin WISP relay at /.well-known/wisp (WebSocket->TCP)")
	cmd.Flags().StringVar(&wispPeer, "wisp-peer", "23.184.48.86:1443", "fallback public Yggdrasil peer endpoint (IP:port)")
	cmd.Flags().StringVar(&wispPeersFile, "wisp-peers-file", "", "path to yggdrasil peers file to whitelist for outbound WISP relay")
	cmd.Flags().StringVar(&wispEmcDomain, "wisp-emc-domain", "", "Emercoin DNS domain for dynamic peer discovery; empty disables discovery")
	cmd.Flags().StringVar(&wispEmcDNS, "wisp-emc-dns", "", "Emercoin DNS server endpoint ([addr]:port) used for peer discovery")
	cmd.Flags().StringVar(&wispDeny, "wisp-deny", "", "comma-separated CIDR prefixes the WISP relay must never dial (e.g. the operator's own networks)")
	cmd.Flags().BoolVar(&allow9p, "wisp-allow-9p", true, "allow outbound 9P connections on port 564 to public IPs")
	cmd.Flags().BoolVar(&allowGrid, "wisp-allow-grid", true, "allow outbound Plan 9 grid connections (rcpu:17019, cpu:17010, auth:567)")
	cmd.Flags().Var(&caCerts, "wisp-ca-cert", "path to a PEM file of trusted CA root(s) that gate the relay; repeatable.")
	return cmd
}

// stringAccum is a repeatable string flag.Value (std flag has no slice type).
type stringAccum []string

func (s *stringAccum) String() string {
	if s == nil {
		return ""
	}
	return strings.Join(*s, ",")
}
func (s *stringAccum) Set(v string) error {
	*s = append(*s, v)
	return nil
}

// buildCertAuth reads the PEM CA root files collected by --wisp-ca-cert into a
// wisp.CertAuth. Returns nil (open relay) when none are given or none parse, so
// local dev stays zero-config.
func buildCertAuth(paths []string) *wisp.CertAuth {
	var roots [][]byte
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			fmt.Fprintf(os.Stderr, "wanix: ignoring unreadable --wisp-ca-cert %q: %v\n", p, err)
			continue
		}
		roots = append(roots, b)
	}
	if len(roots) == 0 {
		return nil
	}
	a := &wisp.CertAuth{Roots: roots}
	if !a.Enabled() {
		fmt.Fprintf(os.Stderr, "wanix: --wisp-ca-cert provided no parseable CA roots; relay stays open\n")
		return nil
	}
	return a
}

// wanixConfigHandler serves a small JSON document advertising the same-origin
// WISP relay path. Pages built with wanix fetch /.well-known/wanix on boot; if
// it responds, the page knows its origin is served by wanix and auto-points the
// v86 network backend at the relay (building the full wisp://<its-host> URL
// from its own location, so http vs https is handled client-side). A 404 (any
// non-wanix host) tells the page to fall back to fetch / a grid relay.
//
// When auth is enabled, the document also advertises "auth":"cert" and the
// auth endpoint path; the page then runs the cert challenge flow (fetch nonce ->
// guest signs sha256(nonce) with its cert key -> POST cert+sig -> open authed WS
// with the returned token) before connecting. Without auth, only "wisp" is
// advertised and the page connects directly.
func wanixConfigHandler(auth *wisp.CertAuth) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Cache-Control", "no-store")
		doc := map[string]string{"wisp": "/.well-known/wisp"}
		if auth != nil && auth.Enabled() {
			doc["auth"] = "cert"
			doc["auth-endpoint"] = "/.well-known/wisp/auth"
		}
		_ = json.NewEncoder(w).Encode(doc)
	})
}

// serve9P answers every connection on ln with a 9P2000 session of fsys,
// until ln is closed.
func serve9P(fsys fs.FS, ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			log.Println("guest 9P accept:", err)
			return
		}
		go func() {
			defer c.Close()
			if err := p9p.Serve(fsys, c, c); err != nil {
				log.Println("guest 9P session ended:", err)
			}
		}()
	}
}

// p9Handler serves fsys over 9P2000 on a WebSocket, one 9P message per
// binary frame.
func p9Handler(fsys fs.FS, w http.ResponseWriter, r *http.Request) {
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		log.Println(err)
		return
	}
	defer ws.Close()

	log.Println("9p session started")
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()

	go func() {
		for {
			typ, buf, err := ws.ReadMessage()
			if err != nil {
				log.Println("ws->9p:", err)
				break
			}
			if typ != websocket.BinaryMessage {
				continue
			}
			if _, err := inW.Write(buf); err != nil {
				log.Println("ws->9p:", err)
				break
			}
		}
	}()

	go func() {
		for {
			// Read message length (4 bytes)
			sizeBuf := make([]byte, 4)
			_, err := io.ReadFull(outR, sizeBuf)
			if err != nil {
				log.Println("9p->ws:", err)
				break
			}
			messageSize := int(sizeBuf[3])<<24 | int(sizeBuf[2])<<16 | int(sizeBuf[1])<<8 | int(sizeBuf[0])
			payloadSize := messageSize - 4

			messageBuf := make([]byte, payloadSize)
			_, err = io.ReadFull(outR, messageBuf)
			if err != nil {
				log.Println("9p->ws:", err)
				break
			}

			buf := append(sizeBuf, messageBuf...)
			if err := ws.WriteMessage(websocket.BinaryMessage, buf); err != nil {
				log.Println("9p->ws:", err)
				break
			}
		}
	}()

	if err := p9p.Serve(fsys, inR, outW); err != nil {
		log.Println("9p session ended:", err)
	}
}

func export9pHandler() http.Handler {
	return altws.Handler(func(conn *altws.Conn) {
		conn.PayloadType = altws.BinaryFrame
		defer conn.Close()

		sess := mux.New(conn)

		l, err := net.Listen("tcp4", ":0")
		if err != nil {
			log.Fatal(err)
		}
		addr := l.Addr().(*net.TCPAddr).String()
		log.Println("9p export on", addr)

		for {
			c, err := l.Accept()
			if err != nil {
				log.Println("9p export accept:", err)
				break
			}
			go func(c net.Conn) {
				defer c.Close()

				ch, err := sess.Open(context.Background())
				if err != nil {
					log.Println("sess open:", err)
					return
				}
				defer ch.Close()

				go io.Copy(ch, c)
				io.Copy(c, ch)
			}(c)
		}
	})
}

func ethernetHandler(vn *vnet.VirtualNetwork) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if vn == nil {
			http.Error(w, "ethernet not available", http.StatusNotFound)
			return
		}
		if !websocket.IsWebSocketUpgrade(r) {
			http.Error(w, "expecting websocket upgrade", http.StatusBadRequest)
			return
		}

		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			log.Println(err)
			return
		}
		defer ws.Close()

		log.Println("ethernet session started")

		if err := vn.AcceptQemu(r.Context(), &qemuAdapter{Conn: ws}); err != nil {
			if strings.Contains(err.Error(), "websocket: close") {
				return
			}
			log.Println(err)
			return
		}
	})
}

func checkOriginAllowed(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	return origin == "https://halatha.rentonsoftworks.com" ||
		origin == "http://localhost:7654" ||
		origin == "http://127.0.0.1:7654" ||
		strings.HasPrefix(origin, "http://localhost:")
}

func parsePeersFile(path string) map[string]bool {
	peers := make(map[string]bool)
	if path == "" {
		return peers
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return peers
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		target := line
		if idx := strings.Index(target, "://"); idx != -1 {
			target = target[idx+3:]
		}
		if idx := strings.IndexAny(target, "?#/"); idx != -1 {
			target = target[:idx]
		}
		target = strings.TrimSpace(target)
		if target != "" {
			if _, _, err := net.SplitHostPort(target); err == nil {
				peers[target] = true
			}
		}
	}
	return peers
}

// validPort reports whether s is a syntactically valid TCP port. The alias
// path discards the port, but it must still reject a malformed one rather
// than treating "192.0.2.9:notaport" as a request for the mount.
func validPort(s string) bool {
	n, err := strconv.Atoi(s)
	return err == nil && n >= 0 && n <= 65535
}

// guest9PAlias is the fixed address a guest dials to reach the host's
// private 9P mount through the relay.
//
// It must be an IPv4 literal, not a name: v86's WISP client sends the
// destination as a dotted quad taken straight from the IP header
// (`hostname: b.ipv4.dest.join(".")` in v86/lib/libv86.mjs), so the guest's
// TCP stack can only ever express an address. A hostname alias could never
// arrive.
//
// 192.0.2.0/24 is TEST-NET-1 (RFC 5737): reserved for documentation and
// guaranteed never to be routable, so this cannot collide with a real
// destination the guest might legitimately want.
const guest9PAlias = "192.0.2.9"

// parseDenyList parses a comma-separated list of CIDR prefixes. Each entry
// must be a prefix, so a mistyped bare address fails loudly instead of
// quietly denying nothing.
func parseDenyList(s string) ([]*net.IPNet, error) {
	var nets []*net.IPNet
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		_, n, err := net.ParseCIDR(part)
		if err != nil {
			return nil, fmt.Errorf("wisp deny list: %w", err)
		}
		nets = append(nets, n)
	}
	return nets, nil
}

// buildWispSafeDialer returns the relay's dialer. deny lists networks the
// relay must never reach beyond the fixed internal ones, such as the
// operator's own machines; it comes from configuration, never the source.
func buildWispSafeDialer(peersFile, defaultPeer, emcDomain, emcDNS string, allow9p, allowGrid bool, guest9PTarget *atomic.Value, logger *log.Logger, deny []*net.IPNet) func(network, addr string) (net.Conn, error) {
	var (
		vpsYggIP = net.ParseIP("201:dd03:96a:1c97:7b9a:98ee:c9ab:1e60")

		peersMu      sync.RWMutex
		allowedPeers = make(map[string]bool)
		lastModTime  time.Time
	)

	reloadPeers := func() {
		if peersFile == "" {
			return
		}
		fi, err := os.Stat(peersFile)
		if err != nil {
			return
		}
		peersMu.RLock()
		needsReload := fi.ModTime().After(lastModTime)
		peersMu.RUnlock()

		if needsReload {
			peersMu.Lock()
			if fi.ModTime().After(lastModTime) {
				allowedPeers = parsePeersFile(peersFile)
				lastModTime = fi.ModTime()
				if logger != nil {
					logger.Printf("wisp: loaded %d allowed dynamic Yggdrasil peers from %s", len(allowedPeers), peersFile)
				}
			}
			peersMu.Unlock()
		}
	}

	reloadPeers()

	return func(network, addr string) (net.Conn, error) {
		reloadPeers()

		// The guest's private 9P mount. The guest connects to the fixed
		// hostname "host9p" and never supplies an address; we substitute
		// the one we chose when we bound the listener.
		//
		// Match on the HOST component only, with any port: a WISP CONNECT
		// carries hostname and port separately and the relay always joins
		// them (wisp/relay.go handleConnect), so the dialer only ever sees
		// "host9p:<port>" -- an exact match on the bare alias could never
		// fire. The port the guest names is discarded; we dial the listener
		// we bound.
		//
		// This is why it is safe to reach loopback here while every other
		// loopback dial stays banned below: there is no attacker-controlled
		// destination on this path. Only this exact hostname is special, so
		// a guest that spells out a real address gets the normal policy and
		// the alias cannot smuggle one.
		// Compare parsed IPs, not strings: "192.0.2.9", "192.0.2.009" and
		// "[::ffff:192.0.2.9]" are all the same destination, and a string
		// match would let one spelling through while blocking the others.
		aliasHost, aliasPort, aliasErr := net.SplitHostPort(addr)
		if aliasErr == nil && net.ParseIP(aliasHost) != nil &&
			net.ParseIP(aliasHost).Equal(net.ParseIP(guest9PAlias)) &&
			validPort(aliasPort) {
			if guest9PTarget == nil {
				return nil, fmt.Errorf("wisp: %s is not enabled", guest9PAlias)
			}
			target, _ := guest9PTarget.Load().(string)
			if target == "" {
				return nil, fmt.Errorf("wisp: %s is not enabled", guest9PAlias)
			}
			if logger != nil {
				logger.Printf("wisp: %s -> %s (guest 9P mount)", guest9PAlias, target)
			}
			return net.DialTimeout("tcp", target, 8*time.Second)
		}

		host, portStr, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		port, err := strconv.Atoi(portStr)
		if err != nil {
			return nil, err
		}

		peersMu.RLock()
		isAuthorizedPeer := allowedPeers[addr] || (defaultPeer != "" && addr == defaultPeer)
		peersMu.RUnlock()

		// Determine authorized public peer (resolve via emcdns if configured)
		if !isAuthorizedPeer && emcDomain != "" && emcDNS != "" {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			r := &net.Resolver{
				PreferGo: true,
				Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
					return net.DialTimeout("udp", emcDNS, 1500*time.Millisecond)
				},
			}
			ips, err := r.LookupHost(ctx, emcDomain)
			cancel()
			if err == nil && len(ips) > 0 {
				authPort := "1443"
				if _, p, err := net.SplitHostPort(defaultPeer); err == nil {
					authPort = p
				}
				resolvedPeer := net.JoinHostPort(ips[0], authPort)
				if addr == resolvedPeer || (host == ips[0] && portStr == authPort) {
					isAuthorizedPeer = true
				}
			}
		}

		is9p := allow9p && port == 564
		isGrid := allowGrid && (port == 17019 || port == 17010 || port == 567)

		if !isAuthorizedPeer && !is9p && !isGrid {
			if logger != nil {
				logger.Printf("SECURITY_ALERT [WISP_PROBE_BLOCKED]: unauthorized dial to %s (only dynamic Yggdrasil peers, 9P:564, or grid allowed)", addr)
			}
			return nil, fmt.Errorf("wisp: egress to %s is prohibited by security policy", addr)
		}

		// Resolve host IPs to verify they are not internal/loopback/homelab
		ips, err := net.LookupIP(host)
		if err != nil {
			parsed := net.ParseIP(host)
			if parsed != nil {
				ips = []net.IP{parsed}
			} else {
				return nil, err
			}
		}

		for _, ip := range ips {
			// Block loopback, link-local, private RFC 1918
			if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsPrivate() {
				if logger != nil {
					logger.Printf("SECURITY_ALERT [WISP_INTERNAL_BLOCKED]: dial to private IP %s (%s)", ip, addr)
				}
				return nil, fmt.Errorf("wisp: private address prohibited")
			}
			// Block the configured deny list (--wisp-deny)
			for _, n := range deny {
				if n.Contains(ip) {
					if logger != nil {
						logger.Printf("SECURITY_ALERT [WISP_DENIED]: dial to denied network %s (%s)", ip, addr)
					}
					return nil, fmt.Errorf("wisp: destination prohibited")
				}
			}
			// Block VPS self Yggdrasil IP
			if ip.Equal(vpsYggIP) {
				return nil, fmt.Errorf("wisp: destination prohibited")
			}
		}

		// Dial the IP that was actually vetted above, not the hostname.
		// Dialing by name would resolve a second time, letting a DNS
		// rebinding answer point the connection at an address the checks
		// never saw (TOCTOU).
		var dialErr error
		for _, ip := range ips {
			conn, err := net.DialTimeout(network, net.JoinHostPort(ip.String(), portStr), 8*time.Second)
			if err == nil {
				return conn, nil
			}
			dialErr = err
		}
		if dialErr == nil {
			dialErr = fmt.Errorf("wisp: no address for %s", host)
		}
		return nil, dialErr
	}
}

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin:     checkOriginAllowed,
}

// qemuAdapter wraps a websocket connection and converts
// messages to qemu length prefixed protocol and back
type qemuAdapter struct {
	*websocket.Conn
	mu          sync.Mutex
	readBuffer  []byte
	writeBuffer []byte
	readOffset  int
}

func (q *qemuAdapter) Read(p []byte) (n int, err error) {
	if len(q.readBuffer) == 0 {
		_, message, err := q.ReadMessage()
		if err != nil {
			return 0, err
		}
		length := uint32(len(message))
		lengthPrefix := make([]byte, 4)
		binary.BigEndian.PutUint32(lengthPrefix, length)
		q.readBuffer = append(lengthPrefix, message...)
		q.readOffset = 0
	}

	n = copy(p, q.readBuffer[q.readOffset:])
	q.readOffset += n
	if q.readOffset >= len(q.readBuffer) {
		q.readBuffer = nil
	}
	return n, nil
}

func (q *qemuAdapter) Write(p []byte) (int, error) {
	q.mu.Lock()
	defer q.mu.Unlock()

	q.writeBuffer = append(q.writeBuffer, p...)

	if len(q.writeBuffer) < 4 {
		return len(p), nil
	}

	length := binary.BigEndian.Uint32(q.writeBuffer[:4])
	// Compare in uint64 so a hostile length cannot overflow int on 32-bit
	// builds and slip past this bounds check.
	if uint64(len(q.writeBuffer)) < uint64(length)+4 {
		return len(p), nil
	}

	err := q.WriteMessage(websocket.BinaryMessage, q.writeBuffer[4:4+length])
	if err != nil {
		return 0, err
	}

	q.writeBuffer = q.writeBuffer[4+length:]
	return len(p), nil
}

func (c *qemuAdapter) LocalAddr() net.Addr {
	return &net.UnixAddr{}
}

func (c *qemuAdapter) RemoteAddr() net.Addr {
	return &net.UnixAddr{}
}

func (c *qemuAdapter) SetDeadline(t time.Time) error {
	return nil
}
func (c *qemuAdapter) SetReadDeadline(t time.Time) error {
	return nil
}
func (c *qemuAdapter) SetWriteDeadline(t time.Time) error {
	return nil
}
