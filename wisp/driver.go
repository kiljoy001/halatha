//go:build !js && !wasm

package wisp

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"

	"tractor.dev/wanix"
	"tractor.dev/wanix/fs"
)

// DefaultListen is the relay's listen address when neither a task arg nor the
// WISP_LISTEN env var specifies one.
const DefaultListen = ":7655"

// Driver implements wanix.TaskDriver for the "wisp" task kind. Start() runs a
// WISP relay (WebSocket→TCP) as an http.Server; the relay lives for the task's
// lifetime and shuts down with the task context. Check() is always false: wisp
// is an explicit kind (allocated via #task/new/wisp), never auto-detected.
//
// Register on the root task: root.Register("wisp", &wisp.Driver{}).
type Driver struct{}

var _ wanix.TaskDriver = (*Driver)(nil)

// Check is always false — wisp is never selected by the auto driver.
func (d *Driver) Check(*wanix.Task) bool { return false }

// Start runs the WISP relay. Listen address resolution (first non-empty wins):
// the task's first arg (Arg(0), from the cmd line), the WISP_LISTEN env var,
// then DefaultListen (":7655").
func (d *Driver) Start(t *wanix.Task) error {
	addr := resolveListenAddr(t)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("wisp: listen %s: %w", addr, err)
	}

	relay := &Relay{Logger: log.New(os.Stderr, "wisp: ", log.LstdFlags|log.Lmsgprefix)}
	srv := &http.Server{Handler: relay}
	wanix.SetWorker(t, srv)

	// Surface the URL the browser should point WISP_RELAY at.
	relayURL := listenURL(ln)
	log.Printf("wisp: relay task #%s listening on %s -> %s", t.ID(), ln.Addr(), relayURL)

	go func() {
		err := srv.Serve(ln)
		if err != nil && err != http.ErrServerClosed {
			log.Printf("wisp: server: %v", err)
		}
		d.recordExit(t, err)
	}()

	// Shut the server down when the task's context is canceled (kernel exit).
	go func() {
		<-t.Context().Done()
		_ = srv.Close()
		_ = ln.Close()
	}()

	return nil
}

func (d *Driver) recordExit(t *wanix.Task, serveErr error) {
	code := 0
	if serveErr != nil && serveErr != http.ErrServerClosed {
		code = 1
	}
	f, err := t.Open("exit")
	if err != nil {
		return
	}
	_, _ = fs.Write(f, []byte(strconv.Itoa(code)))
	_ = f.Close()
}

func resolveListenAddr(t *wanix.Task) string {
	if a := t.Arg(0); a != "" {
		return a
	}
	const prefix = "WISP_LISTEN="
	for _, e := range t.Env() {
		if strings.HasPrefix(e, prefix) {
			return strings.TrimPrefix(e, prefix)
		}
	}
	return DefaultListen
}

// listenURL renders the wisp:// URL the browser should use, preferring the
// advertised host:port over the bound address when the bind is :port.
func listenURL(ln net.Listener) string {
	host, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		return "wisp://localhost:7655"
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "localhost"
	}
	return "wisp://" + net.JoinHostPort(host, port)
}
