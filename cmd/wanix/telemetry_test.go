//go:build !js && !wasm

package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func postTelemetry(t *testing.T, h http.HandlerFunc, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/telemetry", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

// TestTelemetrySessionIDContainsNoTraversal proves a hostile session_id
// cannot escape the sessions directory. The id regex permits '.', so this
// pins that dot-only and embedded-dot ids still resolve inside sessionsDir.
func TestTelemetrySessionIDContainsNoTraversal(t *testing.T) {
	dir := t.TempDir()
	h := telemetryHandler(dir, nil)
	sessions := filepath.Join(dir, "sessions")

	for _, id := range []string{"....", ".....", "a..b", "..--..", "....etc"} {
		body := fmt.Sprintf(`{"session_id":%q,"events":[{"t":1,"type":"cmd","line":"x"}]}`, id)
		if got := postTelemetry(t, h, body).Code; got != http.StatusNoContent {
			t.Fatalf("id %q: status = %d, want 204", id, got)
		}
	}

	// Every file written must live directly under sessions/.
	err := filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		if filepath.Base(p) == "audit.log" {
			return nil
		}
		if got := filepath.Dir(p); got != sessions {
			return fmt.Errorf("telemetry wrote outside sessions dir: %s", p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestTelemetryRejectsBadSessionID pins the id validation itself.
func TestTelemetryRejectsBadSessionID(t *testing.T) {
	h := telemetryHandler(t.TempDir(), nil)
	bad := []string{
		"../../etc/passwd",
		"..",
		"a/b",
		"abc",                   // under 4 chars
		strings.Repeat("a", 65), // over 64 chars
		"with space",
		"semi;colon",
		"",
	}
	for _, id := range bad {
		body := fmt.Sprintf(`{"session_id":%q,"events":[]}`, id)
		if got := postTelemetry(t, h, body).Code; got != http.StatusBadRequest {
			t.Errorf("id %q: status = %d, want 400", id, got)
		}
	}
}

// TestTelemetryAcceptsValidSessionAndWritesJSONL pins the happy path: one
// JSON object per event, enriched with session metadata.
func TestTelemetryAcceptsValidSessionAndWritesJSONL(t *testing.T) {
	dir := t.TempDir()
	h := telemetryHandler(dir, nil)
	body := `{"session_id":"halatha-abc123","image":"9legacy.img","user_agent":"UA/1.0",
	          "events":[{"t":1,"type":"cmd","line":"ls"},{"t":2,"type":"wisp_dial","target":"1.2.3.4:80"}]}`
	if got := postTelemetry(t, h, body).Code; got != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", got)
	}

	data, err := os.ReadFile(filepath.Join(dir, "sessions", "halatha-abc123.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d jsonl lines, want 2", len(lines))
	}
	for _, ln := range lines {
		var m map[string]any
		if err := json.Unmarshal([]byte(ln), &m); err != nil {
			t.Fatalf("line is not valid JSON: %v", err)
		}
		if m["session_id"] != "halatha-abc123" {
			t.Errorf("session_id = %v", m["session_id"])
		}
	}
}

// TestTelemetryRejectsNonPost pins method handling, including the CORS
// preflight that the browser sensor relies on.
func TestTelemetryRejectsNonPost(t *testing.T) {
	h := telemetryHandler(t.TempDir(), nil)

	req := httptest.NewRequest(http.MethodGet, "/api/telemetry", nil)
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET status = %d, want 405", rec.Code)
	}

	req = httptest.NewRequest(http.MethodOptions, "/api/telemetry", nil)
	rec = httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Errorf("OPTIONS status = %d, want 204", rec.Code)
	}
}

// TestTelemetryRejectsMalformedJSON pins parser robustness on untrusted input.
func TestTelemetryRejectsMalformedJSON(t *testing.T) {
	h := telemetryHandler(t.TempDir(), nil)
	for _, body := range []string{"", "{", "null", "[]", `{"session_id":123}`, "\x00\xff"} {
		if got := postTelemetry(t, h, body).Code; got != http.StatusBadRequest {
			t.Errorf("body %q: status = %d, want 400", body, got)
		}
	}
}

// TestTelemetryBodyLimit proves the 1MiB LimitReader truncates oversized
// uploads rather than buffering them without bound.
func TestTelemetryBodyLimit(t *testing.T) {
	h := telemetryHandler(t.TempDir(), nil)
	huge := `{"session_id":"floodtest01","events":[` +
		strings.Repeat(`{"t":1,"type":"cmd","line":"`+strings.Repeat("A", 1000)+`"},`, 3000)
	// Truncated by the limit reader => invalid JSON => 400, never OOM.
	if got := postTelemetry(t, h, huge).Code; got != http.StatusBadRequest {
		t.Errorf("oversized body status = %d, want 400", got)
	}
}

// TestTelemetryClientIPResolution pins the attribution chain. This is a
// honeypot: the recorded client IP is the point of the log, and the
// X-Real-IP / X-Forwarded-For / RemoteAddr precedence must not drift.
func TestTelemetryClientIPResolution(t *testing.T) {
	cases := []struct {
		name       string
		realIP     string
		forwarded  string
		remoteAddr string
		want       string
	}{
		{"x-real-ip wins", "9.9.9.9", "8.8.8.8", "7.7.7.7:1234", "9.9.9.9"},
		{"falls back to forwarded", "", "8.8.8.8", "7.7.7.7:1234", "8.8.8.8"},
		{"forwarded takes first hop", "", "8.8.8.8, 6.6.6.6", "7.7.7.7:1234", "8.8.8.8"},
		{"forwarded trims space", "", "  8.8.8.8  , 6.6.6.6", "7.7.7.7:1234", "8.8.8.8"},
		{"falls back to remote addr", "", "", "7.7.7.7:1234", "7.7.7.7"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			h := telemetryHandler(dir, nil)
			body := `{"session_id":"iptest0001","events":[{"t":1,"type":"cmd","line":"whoami"}]}`
			req := httptest.NewRequest(http.MethodPost, "/api/telemetry", strings.NewReader(body))
			if tc.realIP != "" {
				req.Header.Set("X-Real-IP", tc.realIP)
			}
			if tc.forwarded != "" {
				req.Header.Set("X-Forwarded-For", tc.forwarded)
			}
			req.RemoteAddr = tc.remoteAddr
			rec := httptest.NewRecorder()
			h(rec, req)
			if rec.Code != http.StatusNoContent {
				t.Fatalf("status = %d", rec.Code)
			}

			data, err := os.ReadFile(filepath.Join(dir, "sessions", "iptest0001.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			var m map[string]any
			if err := json.Unmarshal([]byte(strings.TrimSpace(string(data))), &m); err != nil {
				t.Fatal(err)
			}
			if m["client_ip"] != tc.want {
				t.Errorf("client_ip = %v, want %v", m["client_ip"], tc.want)
			}
		})
	}
}

// TestTelemetryAuditLogRecordsEvents pins which event types reach audit.log
// and in what shape. Silently dropping these would blind the honeypot.
func TestTelemetryAuditLogRecordsEvents(t *testing.T) {
	dir := t.TempDir()
	h := telemetryHandler(dir, nil)
	body := `{"session_id":"auditlog01","image":"img.img","user_agent":"UA",
	  "events":[
	    {"t":1,"type":"session_start"},
	    {"t":2,"type":"cmd","line":"cat /adm/keys"},
	    {"t":3,"type":"paste","text":"secret","length":6},
	    {"t":4,"type":"signal","key":"c","line":"partial"},
	    {"t":5,"type":"p9","op":"open","path":"/n/host/clip/text"},
	    {"t":6,"type":"wisp_dial","target":"evil.example:443"},
	    {"t":7,"type":"key","key":"a"}
	  ]}`
	if got := postTelemetry(t, h, body).Code; got != http.StatusNoContent {
		t.Fatalf("status = %d", got)
	}

	data, err := os.ReadFile(filepath.Join(dir, "audit.log"))
	if err != nil {
		t.Fatal(err)
	}
	log := string(data)
	for _, want := range []string{
		"SESSION_START [image=img.img]",
		"COMMAND_EXECUTED: cat /adm/keys",
		"CLIPBOARD_PASTE:",
		"SIGNAL_INTERRUPT: c",
		"9P_FILESYSTEM [op=open]: /n/host/clip/text",
		"NETWORK_DIAL: evil.example:443",
		"auditlog01",
	} {
		if !strings.Contains(log, want) {
			t.Errorf("audit.log missing %q\ngot:\n%s", want, log)
		}
	}
	// "key" events are high volume and deliberately not audited.
	if strings.Contains(log, `"a"`) && strings.Count(log, "\n") > 6 {
		t.Errorf("key events should not reach audit.log:\n%s", log)
	}
}

// TestTelemetryPasteTruncation pins the 100-char preview cap so a hostile
// paste cannot flood audit.log with one enormous line.
func TestTelemetryPasteTruncation(t *testing.T) {
	dir := t.TempDir()
	h := telemetryHandler(dir, nil)
	long := strings.Repeat("Z", 500)
	body := fmt.Sprintf(`{"session_id":"pastetest1","events":[{"t":1,"type":"paste","text":%q,"length":500}]}`, long)
	if got := postTelemetry(t, h, body).Code; got != http.StatusNoContent {
		t.Fatalf("status = %d", got)
	}

	data, err := os.ReadFile(filepath.Join(dir, "audit.log"))
	if err != nil {
		t.Fatal(err)
	}
	log := string(data)
	if !strings.Contains(log, "...") {
		t.Error("long paste was not truncated with ellipsis")
	}
	if strings.Contains(log, strings.Repeat("Z", 200)) {
		t.Error("audit.log contains untruncated paste payload")
	}
	// The recorded length must still report the true size.
	if !strings.Contains(log, "len=500") {
		t.Errorf("audit.log lost the true paste length:\n%s", log)
	}
}

// TestAuditLogFiltersRoutine9PNoise pins the split between the audit log and
// the per-session record. A guest boot emits hundreds of thousands of 9P
// walk/open events; before this filter they were 99.98% of audit.log
// (361,447 of 361,501 entries in production) and grew it ~31MB/day, burying
// the commands and dials the honeypot exists to capture.
func TestAuditLogFiltersRoutine9PNoise(t *testing.T) {
	dir := t.TempDir()
	h := telemetryHandler(dir, nil)
	body := `{"session_id":"noisetest1","events":[
	  {"t":1,"type":"p9","op":"walk","path":"/usb/devices"},
	  {"t":2,"type":"p9","op":"open","path":"/dev/cons"},
	  {"t":3,"type":"p9","op":"walk","path":"/386/bin/ls"},
	  {"t":4,"type":"cmd","line":"cat /adm/keys"}
	]}`
	if got := postTelemetry(t, h, body).Code; got != http.StatusNoContent {
		t.Fatalf("status = %d", got)
	}

	audit, err := os.ReadFile(filepath.Join(dir, "audit.log"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(audit), "9P_FILESYSTEM") {
		t.Errorf("routine 9P traffic reached audit.log:\n%s", audit)
	}
	// The real signal must still be there.
	if !strings.Contains(string(audit), "COMMAND_EXECUTED: cat /adm/keys") {
		t.Errorf("audit.log lost the command event:\n%s", audit)
	}

	// Every event, including the routine 9P ones, stays in the session file.
	sess, err := os.ReadFile(filepath.Join(dir, "sessions", "noisetest1.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(strings.TrimSpace(string(sess)), "\n") + 1; n != 4 {
		t.Errorf("session file has %d events, want all 4 retained", n)
	}
	for _, want := range []string{"/usb/devices", "/dev/cons", "/386/bin/ls"} {
		if !strings.Contains(string(sess), want) {
			t.Errorf("session file dropped %q — forensic record must stay complete", want)
		}
	}
}

// TestAuditLogKeepsSensitive9PPaths pins the other half: access to
// credential, clipboard and host-bridge paths must still be audited.
func TestAuditLogKeepsSensitive9PPaths(t *testing.T) {
	sensitive := []string{
		"/adm/keys",
		"/adm/users",
		"/n/host/clip/text",
		"/n/host/uploads/ctl",
		"/n/host/downloads/ctl",
		"/n/host/fetch/ctl",
		"/n/host/ipfs/ctl",
		"/srv/rio.glenda",
		"/mnt/factotum/ctl",
		"/net/tcp/0/data",
		"/mnt/wsys/snarf",
	}
	for _, p := range sensitive {
		if !isSensitive9PPath(p) {
			t.Errorf("path %q should be audited but is filtered out", p)
		}
	}

	routine := []string{
		"/usb/devices",
		"/dev/cons",
		"/386/bin/ls",
		"/tmp/scratch",
		"",
		".",
	}
	for _, p := range routine {
		if isSensitive9PPath(p) {
			t.Errorf("routine path %q should not be audited", p)
		}
	}
}

// TestSensitive9PPathReachesAuditLog is the end-to-end counterpart, so the
// filter cannot be satisfied by dropping everything.
func TestSensitive9PPathReachesAuditLog(t *testing.T) {
	dir := t.TempDir()
	h := telemetryHandler(dir, nil)
	body := `{"session_id":"sensitive1","events":[
	  {"t":1,"type":"p9","op":"open","path":"/n/host/clip/text"}
	]}`
	if got := postTelemetry(t, h, body).Code; got != http.StatusNoContent {
		t.Fatalf("status = %d", got)
	}
	audit, err := os.ReadFile(filepath.Join(dir, "audit.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(audit), "/n/host/clip/text") {
		t.Errorf("clipboard access was not audited:\n%s", audit)
	}
}

// TestGuestHealthReachesAuditLog pins the one signal that can only come from
// inside the VM. Namespace-class bugs -- a bind that landed in a namespace
// rio already forked away from, a /dev/snarf that is not in this namespace
// -- are invisible from the host, so if these events are dropped there is
// no way to detect them at all.
func TestGuestHealthReachesAuditLog(t *testing.T) {
	dir := t.TempDir()
	h := telemetryHandler(dir, nil)
	body := `{"session_id":"healthtest","events":[
	  {"t":1,"type":"health","key":"OK","line":"libtab","text":"VERIFIED: 803 binaries"},
	  {"t":2,"type":"health","key":"FAIL","line":"bin-bound","text":"/bin/troff missing"}
	]}`
	if got := postTelemetry(t, h, body).Code; got != http.StatusNoContent {
		t.Fatalf("status = %d", got)
	}
	audit, err := os.ReadFile(filepath.Join(dir, "audit.log"))
	if err != nil {
		t.Fatal(err)
	}
	log := string(audit)
	for _, want := range []string{
		"GUEST_HEALTH OK libtab",
		"GUEST_HEALTH FAIL bin-bound",
		"/bin/troff missing",
	} {
		if !strings.Contains(log, want) {
			t.Errorf("audit.log missing %q:\n%s", want, log)
		}
	}
}
