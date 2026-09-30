// ygg9p is a 9front stdio service for aux/listen1. It never announces a
// wildcard address and never opens an export before signed authentication.
package main

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"tractor.dev/wanix/internal/ygg9pauth"
)

type exportConfig struct {
	TransportKeys []string `json:"transportKeys,omitempty"`
	PublicKey     string   `json:"publicKey"`
	Root          string   `json:"root"`
	Access        string   `json:"access"`
	Image         string   `json:"image,omitempty"`
	QuotaBytes    int64    `json:"quotaBytes,omitempty"`
}
type configuration struct {
	ServerKey string         `json:"serverKey"`
	Exports   []exportConfig `json:"exports"`
}

func load(path string) (ygg9pauth.Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return ygg9pauth.Config{}, err
	}
	defer f.Close()
	var file configuration
	d := json.NewDecoder(io.LimitReader(f, 65537))
	d.DisallowUnknownFields()
	if err := d.Decode(&file); err != nil {
		return ygg9pauth.Config{}, err
	}
	if d.Decode(new(any)) != io.EOF {
		return ygg9pauth.Config{}, errors.New("trailing configuration data")
	}
	key, err := hex.DecodeString(file.ServerKey)
	if err != nil {
		return ygg9pauth.Config{}, err
	}
	cfg := ygg9pauth.Config{ServerKey: ed25519.PublicKey(key), Keys: make(map[string]ed25519.PublicKey), Exports: make(map[string]ygg9pauth.Export)}
	for _, e := range file.Exports {
		key, err := hex.DecodeString(e.PublicKey)
		if err != nil {
			return cfg, err
		}
		if _, ok := cfg.Keys[e.PublicKey]; ok {
			return cfg, errors.New("duplicate approved key")
		}
		if filepath.Clean(e.Root) != e.Root {
			return cfg, errors.New("export root must be canonical")
		}
		if e.Access == "read-write" {
			base := filepath.Dir(path)
			if e.QuotaBytes != 1<<30 || e.Image != filepath.Join(base, "images", e.PublicKey+".img") || e.Root != filepath.Join(base, "mnt", e.PublicKey, "data") {
				return cfg, errors.New("writable export requires its own 1 GiB image and private mount root")
			}
			st, err := os.Stat(e.Image)
			if err != nil || st.Size() != e.QuotaBytes || st.IsDir() {
				return cfg, errors.New("quota image missing or wrong size")
			}
		} else {
			st, err := os.Stat(e.Root)
			if err != nil || !st.IsDir() {
				return cfg, errors.New("export root is not a directory")
			}
		}
		var transports []ed25519.PublicKey
		for _, id := range e.TransportKeys {
			p, err := publicKey(id)
			if err != nil {
				return cfg, err
			}
			transports = append(transports, p)
		}
		cfg.Keys[e.PublicKey] = ed25519.PublicKey(key)
		cfg.Exports[e.PublicKey] = ygg9pauth.Export{TransportKeys: transports, Root: e.Root, Access: e.Access, Open: func() (io.ReadWriteCloser, error) { return startExport(e) }}
	}
	return cfg, cfg.Validate()
}

type exportProcess struct {
	cmd  *exec.Cmd
	in   io.WriteCloser
	out  io.ReadCloser
	once sync.Once
	err  error
}

func (p *exportProcess) Read(b []byte) (int, error)  { return p.out.Read(b) }
func (p *exportProcess) Write(b []byte) (int, error) { return p.in.Write(b) }
func (p *exportProcess) Close() error {
	p.once.Do(func() { p.in.Close(); p.out.Close(); p.cmd.Process.Kill(); p.err = p.cmd.Wait() })
	return p.err
}
func startExport(e exportConfig) (io.ReadWriteCloser, error) {
	args := []string{"-r", e.Root}
	if e.Access == "read-only" {
		args = append([]string{"-R"}, args...)
	}
	cmd := exec.Command("/bin/exportfs", args...)
	if e.Access == "read-write" {
		// A private namespace prevents one client's mount from affecting another.
		// The script is constant; configuration values are environment data.
		cmd = exec.Command("/bin/rc", "-c", "rfork n; mount -n $yggsrv $yggmnt && exec /bin/exportfs -r $yggroot")
		cmd.Env = append(exportEnvironment(), "yggsrv=/srv/ygg9p-"+e.PublicKey, "yggmnt="+filepath.Dir(e.Root), "yggroot="+e.Root)
	}
	cmd.Stderr = os.Stderr
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		in.Close()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		in.Close()
		out.Close()
		return nil, err
	}
	return &exportProcess{cmd: cmd, in: in, out: out}, nil
}

func exportEnvironment() []string {
	var env []string
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "fn#") {
			env = append(env, e)
		}
	}
	return env
}

type stdioConn struct {
	mu    sync.Mutex
	timer *time.Timer
}

func (*stdioConn) Read(b []byte) (int, error)  { return os.Stdin.Read(b) }
func (*stdioConn) Write(b []byte) (int, error) { return os.Stdout.Write(b) }
func (*stdioConn) Close() error                { os.Stdin.Close(); return os.Stdout.Close() }
func (*stdioConn) LocalAddr() net.Addr         { return &net.TCPAddr{} }
func (*stdioConn) RemoteAddr() net.Addr        { return &net.TCPAddr{} }

// Plan 9 os.File deadlines are unsupported. Each connection has its own
// process; terminating it bounds a stalled authentication exchange.
func (c *stdioConn) SetDeadline(t time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.timer != nil {
		c.timer.Stop()
		c.timer = nil
	}
	if !t.IsZero() {
		c.timer = time.AfterFunc(time.Until(t), func() { os.Exit(124) })
	}
	return nil
}
func (c *stdioConn) SetReadDeadline(t time.Time) error  { return c.SetDeadline(t) }
func (c *stdioConn) SetWriteDeadline(t time.Time) error { return c.SetDeadline(t) }

func endpoint(path string) (net.IP, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	s := strings.TrimSpace(string(b))
	i := strings.LastIndex(s, "!")
	if i < 0 {
		return nil, errors.New("invalid Plan 9 endpoint")
	}
	ip := net.ParseIP(s[:i])
	if ip == nil {
		return nil, errors.New("invalid endpoint address")
	}
	return ip, nil
}

func main() {
	path := flag.String("config", "", "private JSON configuration")
	requestService := flag.Bool("request-service", false, "restricted proposal-only 9P service under aux/listen1")
	pending := flag.Bool("pending", false, "list pending unverified registration/change proposals")
	resolve := flag.String("resolve-request", "", "explicitly remove a reviewed request by ID")
	setTransport := flag.String("set-transport", "", "explicitly update an enrolled client transport key")
	list := flag.Bool("list", false, "list enrolled public keys as tab-separated records")
	revoke := flag.String("revoke", "", "explicitly revoke a client key; retain its backing image")
	check := flag.Bool("check", false, "validate configuration without serving")
	address := flag.String("address", "", "print the node address for a public key")
	allocate := flag.String("allocate", "", "create a NEW fixed-size 1 GiB image for a public key")
	enroll := flag.String("enroll", "", "enroll a public key after provisioning its private image")
	transport := flag.String("transport-key", "", "optional approved forwarding node public key for enrollment")
	stopLegacy := flag.Bool("stop-legacy", false, "9front: drop to none and stop legacy wildcard 9P listeners/root exports")
	flag.Parse()
	if *pending {
		if err := listRequests(*path, os.Stdout); err != nil {
			log.Fatal(err)
		}
		return
	}
	if *resolve != "" {
		if err := resolveRequest(*path, *resolve); err != nil {
			log.Fatal(err)
		}
		return
	}
	if *setTransport != "" {
		if err := updateTransport(*path, *setTransport, *transport); err != nil {
			log.Fatal(err)
		}
		return
	}
	if *list {
		if err := listKeys(*path, os.Stdout); err != nil {
			log.Fatal(err)
		}
		return
	}
	if *revoke != "" {
		if err := revokeKey(*path, *revoke); err != nil {
			log.Fatal(err)
		}
		return
	}
	if *stopLegacy {
		if err := stopLegacyExports(); err != nil {
			log.Fatal(err)
		}
		return
	}
	if *address != "" {
		key, err := publicKey(*address)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println(ygg9pauth.Address(key).String())
		return
	}
	if *allocate != "" {
		if err := allocateImage(*path, *allocate); err != nil {
			log.Fatal(err)
		}
		return
	}
	if *enroll != "" {
		var transports []string
		if *transport != "" {
			transports = []string{*transport}
		}
		if err := enrollKey(*path, *enroll, transports...); err != nil {
			log.Fatal(err)
		}
		return
	}
	cfg, err := load(*path)
	if err != nil {
		log.Fatal(err)
	}
	if *check {
		return
	}
	dir := os.Getenv("net")
	if dir == "" {
		log.Fatal("must run under aux/listen1")
	}
	local, err := endpoint(filepath.Join(dir, "local"))
	if err != nil {
		log.Fatal(err)
	}
	if !local.Equal(ygg9pauth.Address(cfg.ServerKey)) {
		log.Fatal("listener is not bound to server Yggdrasil address")
	}
	peer, err := endpoint(filepath.Join(dir, "remote"))
	if err != nil {
		log.Fatal(err)
	}
	if *requestService {
		if err := requestPeerAllowed(peer, *transport); err != nil {
			log.Fatal(err)
		}
		timer := time.AfterFunc(30*time.Second, func() { os.Exit(124) })
		defer timer.Stop()
		if err := serveRequests(os.Stdin, os.Stdout, *path, peer.String()); err != nil {
			log.Print(err)
		}
		return
	}
	if err := ygg9pauth.Serve(&stdioConn{}, peer, cfg); err != nil {
		log.Print(err)
	}
}

// Requests are unverified proposals. Only overlay nodes may submit them;
// approval and signed filesystem authentication remain separate.
func requestPeerAllowed(peer net.IP, restrictedKey string) error {
	if peer == nil || peer.To4() != nil || peer.To16()[0] != 2 {
		return errors.New("request transport must be a Yggdrasil node")
	}
	if restrictedKey != "" {
		key, err := publicKey(restrictedKey)
		if err != nil || !peer.Equal(ygg9pauth.Address(key)) {
			return errors.New("unapproved request transport")
		}
	}
	return nil
}
