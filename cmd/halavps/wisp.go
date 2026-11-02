package main

// Wisp (WebSocket -> TCP) relay for the v86 guest's virtio network.
//
// The v86 Wisp adapter carries Ethernet frames, answers DHCP/ARP and
// resolves DNS through DoH; this server only proxies the resulting TCP
// streams. A single-use session token is always required.
//
// When the relay listens on a loopback address it is only reachable through
// the local web server and proxies any unicast destination, so the guest can
// reach the public internet. HALAVPS_NET_ALLOW still overrides that with an
// explicit CIDR list. A relay bound to a non-loopback address keeps the
// private-range default: there it really is a proxy others can reach.

import (
	"encoding/binary"
	"log"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	wispConnect = 1
	wispData    = 2
	wispClose   = 4

	wispCloseNormal = 1
	wispCloseError  = 2
)

type wispServer struct {
	server *server
	allow  []*net.IPNet
	open   bool
}

// listenIsLocal reports whether addr is a loopback listener. Such a relay is
// only reachable through the web server in front of it.
func listenIsLocal(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	if host == "localhost" {
		return true
	}
	if host == "" {
		/* ":port" listens on every interface. */
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func newWispServer(s *server, listenAddr string) *wispServer {
	w := &wispServer{server: s}
	raw := strings.TrimSpace(os.Getenv("HALAVPS_NET_ALLOW"))
	if raw == "any" || raw == "all" || raw == "*" {
		w.open = true
		return w
	}
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if _, n, err := net.ParseCIDR(part); err == nil {
			w.allow = append(w.allow, n)
		}
	}
	if len(w.allow) > 0 {
		return w
	}
	if listenIsLocal(listenAddr) {
		w.open = true
		return w
	}
	for _, raw := range []string{
		"10.0.0.0/8",
		"172.16.0.0/12",
		"192.168.0.0/16",
		"100.64.0.0/10",
	} {
		_, n, _ := net.ParseCIDR(raw)
		w.allow = append(w.allow, n)
	}
	return w
}

func (w *wispServer) allowed(ip net.IP) bool {
	if ip == nil || ip.IsUnspecified() || ip.IsLoopback() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() {
		return false
	}
	// Never expose cloud metadata through the relay.
	if ip.Equal(net.ParseIP("169.254.169.254")) {
		return false
	}
	if w.open {
		return true
	}
	for _, n := range w.allow {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

func (w *wispServer) handler(conn *websocket.Conn) {
	defer conn.Close()
	conn.SetReadLimit(1 << 20)

	var (
		mu      sync.Mutex
		streams = make(map[uint32]net.Conn)
	)

	writeFrame := func(kind byte, id uint32, payload []byte) error {
		frame := make([]byte, 5+len(payload))
		frame[0] = kind
		binary.LittleEndian.PutUint32(frame[1:5], id)
		copy(frame[5:], payload)
		mu.Lock()
		defer mu.Unlock()
		return conn.WriteMessage(websocket.BinaryMessage, frame)
	}

	closeStream := func(id uint32, reason byte) {
		mu.Lock()
		c := streams[id]
		delete(streams, id)
		mu.Unlock()
		if c != nil {
			c.Close()
		}
		writeFrame(wispClose, id, []byte{reason})
	}

	connect := func(id uint32, port uint16, host string) {
		if net.ParseIP(host) == nil {
			// The adapter normally sends a literal IPv4 address. Refuse
			// names rather than giving the guest a DNS-based bypass of the
			// allowlist.
			closeStream(id, wispCloseError)
			return
		}
		ip := net.ParseIP(host)
		if !w.allowed(ip) {
			log.Printf("wisp: denied %s:%d from %s", host, port, conn.RemoteAddr())
			closeStream(id, wispCloseError)
			return
		}
		target := net.JoinHostPort(host, strconv.Itoa(int(port)))
		d := net.Dialer{Timeout: 10 * time.Second}
		c, err := d.Dial("tcp", target)
		if err != nil {
			log.Printf("wisp: dial %s: %v", target, err)
			closeStream(id, wispCloseError)
			return
		}
		log.Printf("wisp: %s -> %s", conn.RemoteAddr(), target)
		mu.Lock()
		if _, exists := streams[id]; exists {
			mu.Unlock()
			c.Close()
			return
		}
		streams[id] = c
		mu.Unlock()

		go func() {
			buf := make([]byte, 32<<10)
			for {
				n, err := c.Read(buf)
				if n > 0 {
					if werr := writeFrame(wispData, id, buf[:n]); werr != nil {
						break
					}
				}
				if err != nil {
					closeStream(id, wispCloseNormal)
					return
				}
			}
		}()
	}

	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			break
		}
		for len(msg) > 0 {
			kind := msg[0]
			switch kind {
			case wispConnect:
				if len(msg) < 8 {
					msg = nil
					continue
				}
				id := binary.LittleEndian.Uint32(msg[1:5])
				port := binary.LittleEndian.Uint16(msg[6:8])
				host := string(msg[8:])
				connect(id, port, host)
				msg = nil
			case wispData:
				if len(msg) < 5 {
					msg = nil
					continue
				}
				id := binary.LittleEndian.Uint32(msg[1:5])
				mu.Lock()
				c := streams[id]
				mu.Unlock()
				if c != nil {
					if _, err := c.Write(msg[5:]); err != nil {
						closeStream(id, wispCloseError)
					}
				}
				msg = nil
			case wispClose:
				if len(msg) < 6 {
					msg = nil
					continue
				}
				id := binary.LittleEndian.Uint32(msg[1:5])
				closeStream(id, wispCloseNormal)
				msg = nil
			default:
				// Unknown frame type: drop the message; the Wisp client
				// sends one frame per WebSocket message.
				msg = nil
			}
		}
	}

	mu.Lock()
	for _, c := range streams {
		c.Close()
	}
	streams = nil
	mu.Unlock()
}
