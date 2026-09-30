//go:build !js && !wasm

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

var (
	validSessionIDRe = regexp.MustCompile(`^[a-zA-Z0-9_\-\.]{4,64}$`)
	telemetryMu      sync.Mutex
)

type TelemetryBatch struct {
	SessionID string           `json:"session_id"`
	Timestamp int64            `json:"timestamp"`
	UserAgent string           `json:"user_agent"`
	Screen    string           `json:"screen"`
	Image     string           `json:"image"`
	Events    []TelemetryEvent `json:"events"`
}

type TelemetryEvent struct {
	Timestamp int64  `json:"t"`
	Type      string `json:"type"` // "session_start", "key", "cmd", "paste", "signal", "p9", "tty_output", "wisp_dial"
	Key       string `json:"key,omitempty"`
	Code      string `json:"code,omitempty"`
	Line      string `json:"line,omitempty"`
	Text      string `json:"text,omitempty"`
	Op        string `json:"op,omitempty"`
	Path      string `json:"path,omitempty"`
	Target    string `json:"target,omitempty"`
	Stream    string `json:"stream,omitempty"`
	Length    int    `json:"length,omitempty"`
}

// sensitive9PPaths are namespace locations whose access is worth recording
// in the audit log. Everything else under 9P is routine boot traffic.
var sensitive9PPaths = []string{
	"/adm/",           // Plan 9 admin: users, keys
	"/n/host/clip",    // host clipboard bridge
	"/n/host/uploads", // host file ingress
	"/n/host/downloads",
	"/n/host/fetch", // outbound HTTP proxy
	"/n/host/ipfs",
	"/srv/",     // service registry (rio, factotum)
	"factotum",  // credential agent
	"/net/",     // network stack
	"/mnt/wsys", // rio window system
}

// isSensitive9PPath reports whether a 9P path is worth an audit entry.
func isSensitive9PPath(p string) bool {
	if p == "" {
		return false
	}
	for _, s := range sensitive9PPaths {
		if strings.Contains(p, s) {
			return true
		}
	}
	return false
}

func telemetryHandler(logDir string, logger *log.Logger) http.HandlerFunc {
	sessionsDir := filepath.Join(logDir, "sessions")
	_ = os.MkdirAll(sessionsDir, 0750)
	auditPath := filepath.Join(logDir, "audit.log")

	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		body, err := io.ReadAll(io.LimitReader(r.Body, 1024*1024))
		if err != nil {
			http.Error(w, "Error reading request", http.StatusBadRequest)
			return
		}

		var batch TelemetryBatch
		if err := json.Unmarshal(body, &batch); err != nil {
			http.Error(w, "Invalid JSON payload", http.StatusBadRequest)
			return
		}

		if !validSessionIDRe.MatchString(batch.SessionID) {
			http.Error(w, "Invalid session ID format", http.StatusBadRequest)
			return
		}

		clientIP := r.Header.Get("X-Real-IP")
		if clientIP == "" {
			clientIP = r.Header.Get("X-Forwarded-For")
			if idx := strings.Index(clientIP, ","); idx != -1 {
				clientIP = strings.TrimSpace(clientIP[:idx])
			}
		}
		if clientIP == "" {
			host, _, _ := net.SplitHostPort(r.RemoteAddr)
			clientIP = host
		}

		telemetryMu.Lock()
		defer telemetryMu.Unlock()

		// 1. Append raw events to sessions/<session_id>.jsonl
		sessionFile := filepath.Join(sessionsDir, fmt.Sprintf("%s.jsonl", batch.SessionID))
		if f, err := os.OpenFile(sessionFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0640); err == nil {
			for _, ev := range batch.Events {
				enriched := struct {
					SessionID string `json:"session_id"`
					ClientIP  string `json:"client_ip"`
					UserAgent string `json:"user_agent,omitempty"`
					Image     string `json:"image,omitempty"`
					TelemetryEvent
				}{
					SessionID:      batch.SessionID,
					ClientIP:       clientIP,
					UserAgent:      batch.UserAgent,
					Image:          batch.Image,
					TelemetryEvent: ev,
				}
				if lineBytes, err := json.Marshal(enriched); err == nil {
					f.Write(append(lineBytes, '\n'))
				}
			}
			f.Close()
		}

		// 2. Write high-priority audit events to audit.log and logger
		var auditWriter io.Writer
		if af, err := os.OpenFile(auditPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0640); err == nil {
			auditWriter = af
			defer af.Close()
		}

		for _, ev := range batch.Events {
			var auditMsg string
			switch ev.Type {
			case "session_start":
				auditMsg = fmt.Sprintf("SESSION_START [image=%s] [ua=%s]", batch.Image, batch.UserAgent)
			case "cmd":
				auditMsg = fmt.Sprintf("COMMAND_EXECUTED: %s", ev.Line)
			case "paste":
				preview := ev.Text
				if len(preview) > 100 {
					preview = preview[:100] + "..."
				}
				auditMsg = fmt.Sprintf("CLIPBOARD_PASTE: %q (len=%d)", preview, ev.Length)
			case "signal":
				auditMsg = fmt.Sprintf("SIGNAL_INTERRUPT: %s (partial=%q)", ev.Key, ev.Line)
			case "p9":
				// Ordinary 9P traffic is boot noise: a single guest boot
				// emits hundreds of thousands of walk/open events, which
				// buried the real signal (commands, dials, pastes) at a
				// ratio of ~6700:1 and grew audit.log ~31MB/day. The full
				// record still goes to the per-session JSONL above; only
				// sensitive paths are promoted to the audit log.
				if isSensitive9PPath(ev.Path) {
					auditMsg = fmt.Sprintf("9P_FILESYSTEM [op=%s]: %s", ev.Op, ev.Path)
				}
			case "wisp_dial":
				auditMsg = fmt.Sprintf("NETWORK_DIAL: %s", ev.Target)
			case "health":
				// Guest self-check result. The guest is the only thing that
				// can see namespace-class failures (whether /bin got the
				// libtab bind, whether /dev/snarf is reachable), so these
				// always reach the audit log.
				auditMsg = fmt.Sprintf("GUEST_HEALTH %s %s %s", ev.Key, ev.Line, ev.Text)
			}

			if auditMsg != "" {
				now := time.Now().UTC().Format(time.RFC3339)
				logLine := fmt.Sprintf("%s halatha-sensor[%s]: [IP %s] %s\n", now, batch.SessionID, clientIP, auditMsg)
				if auditWriter != nil {
					auditWriter.Write([]byte(logLine))
				}
				if logger != nil {
					logger.Print(logLine)
				}
			}
		}

		w.WriteHeader(http.StatusNoContent)
	}
}
