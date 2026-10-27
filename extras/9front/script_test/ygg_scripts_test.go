package script_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const scriptRoot = ".."

func TestYggRcScriptsParseWithPlan9Rc(t *testing.T) {
	rc := plan9Rc(t)
	for _, script := range []string{
		"halatha.local",
		"hostbin.rc",
		"ygg-up.rc",
		"ygg-peers.rc",
		"pipeline/halatha-grid-terminal.rc",
		"pipeline/install-halatha-http.rc",
	} {
		t.Run(script, func(t *testing.T) {
			probe := strings.Join([]string{
				"fn test { exit no }",
				"fn hget { exit no }",
				"fn mkdir { exit '' }",
				"fn chmod { exit '' }",
				"fn cp { exit '' }",
				"fn rm { exit '' }",
				"fn grep { exit no }",
				"fn sleep { exit '' }",
				"fn ip/ipconfig { exit '' }",
				"fn yggdrasilctl { exit no }",
				"fn hoc { echo 1 }",
				". " + filepath.Join(scriptRoot, script),
			}, "\n")
			cmd := exec.Command(rc, "-c", probe)
			var out bytes.Buffer
			cmd.Stdout = &out
			cmd.Stderr = &out
			err := cmd.Run()
			text := out.String()
			if strings.Contains(text, "syntax error") ||
				strings.Contains(text, "token") ||
				strings.Contains(text, "missing") && strings.Contains(text, "}") {
				t.Fatalf("%s has rc parse error: %v\n%s", script, err, text)
			}
		})
	}
}

func TestYggScriptsAvoidRcKeywordAssignments(t *testing.T) {
	keywords := regexp.MustCompile(`(?m)^\s*(if|if not|for|fn|switch|while|case|in)=`)
	for _, script := range []string{"halatha.local", "ygg-up.rc", "ygg-peers.rc"} {
		body := read(t, script)
		if match := keywords.FindString(body); match != "" {
			t.Fatalf("%s assigns to rc keyword: %q", script, match)
		}
	}
}

// TestYggAdminSocketSetup pins the Yggdrasil bring-up contract. Yggdrasil
// startup lives in ygg-up.rc; halatha.local is only a boot hook and
// deliberately carries none of it.
func TestYggAdminSocketSetup(t *testing.T) {
	body := read(t, "ygg-up.rc")
	mustContain(t, "ygg-up.rc", body, "ip/ipconfig loopback /dev/null 127.1 /128")
	mustContain(t, "ygg-up.rc", body, "AdminListen: tcp://127.0.0.1:9001")
	// The daemon runs a LOCAL copy, not /386/bin/yggdrasil: that path is a
	// union entry backed by the /n/host/bin 9P mount, and Plan 9
	// demand-pages executables, so a daemon run from there keeps faulting
	// text pages over 9P for its whole life.
	mustContain(t, "ygg-up.rc", body, "$YGG -useconffile $RUNCONF")
	mustContain(t, "ygg-up.rc", body, "cp /386/bin/yggdrasil $YGG")
	mustContain(t, "ygg-up.rc", body, "/rc/bin/ygg-peers")
	// awk is not in the base image; the scripts must not depend on it.
	mustNotContain(t, "ygg-up.rc", body, "awk '")

	peers := read(t, "ygg-peers.rc")
	mustContain(t, "ygg-peers.rc", peers, "yggdrasilctl -endpoint tcp://127.0.0.1:9001")
	mustContain(t, "ygg-peers.rc", peers, "PEER=tls://")
	mustContain(t, "ygg-peers.rc", peers, "addPeer 'uri='$PEER")
}

// TestHalathaLocalStagesHostTools pins halatha.local's actual job: staging
// on-demand tools out of /n/host/boot and union-binding the verified binary
// tree only after libtab reports VERIFIED.
func TestHalathaLocalStagesHostTools(t *testing.T) {
	body := read(t, "halatha.local")
	// The staging loop builds these paths from a name list rather than
	// spelling each one out, so assert the loop and its members.
	mustContain(t, "halatha.local", body, "/n/host/boot/$t^.rc")
	for _, tool := range []string{"ipfs", "clip", "ygg-up", "ygg-peers", "pkg"} {
		if !strings.Contains(body, tool) {
			t.Errorf("halatha.local no longer stages %q", tool)
		}
	}
	mustContain(t, "halatha.local", body, "/n/host/bin/STATUS")
	mustNotContain(t, "halatha.local", body, "awk '")

	// The bind must NOT be here. rio does rfork(RFNAMEG) at startup
	// (rio.c:290) and per window (wind.c:1355), so it snapshots the
	// namespace; a bind from this hook -- which termrc starts before rio --
	// lands in the boot namespace and no window ever sees it. The bind
	// succeeds and logs success while /bin stays empty, which is exactly
	// the bug this guards against.
	mustNotContain(t, "halatha.local", body, "bind -a /n/host/bin")
}

// TestHalathaBindRunsBeforeRio pins where the union bind actually lives: in
// a helper the profile sources ahead of `exec rio`, so rio inherits it.
func TestHalathaBindRunsBeforeRio(t *testing.T) {
	body := read(t, "halatha-bind.rc")

	for _, want := range []string{
		"bind -a /n/host/bin /bin",
		"bind -a /n/host/bin /386/bin",
		"/n/host/bin/STATUS",
	} {
		mustContain(t, "halatha-bind.rc", body, want)
	}

	// Still gated on libtab verification -- never bind an unverified tree.
	mustContain(t, "halatha-bind.rc", body, "VERIFIED:*")

	// rc backticks split on whitespace, so the status must be rejoined
	// before being echoed or the message repeats once per word.
	mustContain(t, "halatha-bind.rc", body, `$"hstatus`)
}

// TestTermrcMountsHostOverVirtio9p pins how the guest reaches the host: the
// kernel's #9 device (devvirtio9p) carries 9P2000 over virtio. The serial
// bridge is gone -- it was far slower -- so nothing may fall back to it.
func TestTermrcMountsHostOverVirtio9p(t *testing.T) {
	body := read(t, "termrc.local")
	mustContain(t, "termrc.local", body, "mount '#9/0' /n/host")
	// A successful mount leaves a marker the host can read back, so the
	// browser can tell "the guest wrote through #9" from "nothing happened".
	mustContain(t, "termrc.local", body, ">/n/host/boot/host9p")
	for _, gone := range []string{"serial9p", "/dev/eia", "/srv/host"} {
		mustNotContain(t, "termrc.local", body, gone)
	}
	// The browser's current hook runs after the mount and seed marker. The
	// installed copy is only a fallback when the browser is unavailable.
	mount := strings.Index(body, "mount '#9/0' /n/host")
	hook := strings.Index(body, "rc /n/host/boot/halatha.local")
	if hook < 0 || hook < mount {
		t.Errorf("termrc.local must start halatha.local after mounting /n/host")
	}
	mustContain(t, "termrc.local", body, "/n/host/boot/seed-ready")
	mustContain(t, "termrc.local", body, "/rc/bin/halatha.local")
}

// TestKernelBuildScriptParses checks kernel/build.rc with plan9port rc
// without running it: the body is wrapped in a function definition, which
// rc must parse but never executes (the script runs mk and cd on /sys).
func TestKernelBuildScriptParses(t *testing.T) {
	rc := plan9Rc(t)
	for _, script := range []string{"kernel/build.rc", "pipeline/install-image.rc"} {
		body := read(t, script)
		body = strings.TrimPrefix(body, "#!/bin/rc\n")
		out, err := exec.Command(rc, "-c", "fn parsecheck {\n"+body+"\n}\necho PARSED").CombinedOutput()
		if err != nil || !strings.Contains(string(out), "PARSED") {
			t.Errorf("%s does not parse: %v\n%s", script, err, out)
		}
	}
}

// TestRuntimeScriptsStaySmall guards the rc footguns that actually break
// these boot scripts: excessive size, bare flag assignments, and unquoted
// `=` (which rc treats as a delimiter, not an argument character).
// Comments are fine in rc and these are maintained source files.
func TestRuntimeScriptsStaySmall(t *testing.T) {
	for _, script := range []string{"halatha.local", "ygg-up.rc", "ygg-peers.rc"} {
		body := read(t, script)
		lines := strings.Split(body, "\n")
		// Count code, not comments: these scripts document hard-won Plan 9
		// behaviour (namespace rules, 9P paging) and that prose is worth
		// keeping. The cap exists to stop the LOGIC growing unwieldy.
		code := 0
		for _, line := range lines {
			t := strings.TrimSpace(line)
			if t != "" && !strings.HasPrefix(t, "#") {
				code++
			}
		}
		limit := 90
		if script == "halatha.local" {
			// The boot hook stages tools, launches services, and reports guest
			// health; keep the narrower cap for the Yggdrasil helpers.
			limit = 150
		}
		if code > limit {
			t.Fatalf("%s too large for fragile boot script: %d code lines", script, code)
		}
		if match := regexp.MustCompile(`(?m)\s-[A-Za-z0-9_-]+=`).FindString(body); match != "" {
			t.Fatalf("%s contains bare flag assignment that 9front rc rejects: %q", script, match)
		}
		for i, line := range lines {
			trimmed := strings.TrimSpace(line)
			// Comments routinely cite C source ("p->state = Broken",
			// "NBROKEN=4"); only rc code can have an rc equals footgun.
			if strings.HasPrefix(trimmed, "#") {
				continue
			}
			if strings.Contains(trimmed, "=") &&
				!regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*=`).MatchString(trimmed) &&
				!strings.Contains(trimmed, "'uri='") &&
				!strings.Contains(trimmed, ">[2=1]") &&
				!strings.Contains(trimmed, ">[2]/dev/null") &&
				!strings.Contains(trimmed, "[ 	]*") {
				t.Fatalf("%s has suspicious equals on line %d: %s", script, i+1, line)
			}
		}
	}
}

func TestPayloadPackagesYggPeerInjectorAndOverwrites(t *testing.T) {
	body := read(t, "pipeline/mk9frontpayload.sh")
	mustContain(t, "pipeline/mk9frontpayload.sh", body, `cp extras/9front/ygg-peers.rc "$work/rc/bin/ygg-peers"`)
	mustContain(t, "pipeline/mk9frontpayload.sh", body, `cp extras/9front/pipeline/install-halatha-http.rc "$work/install/install-halatha-http.rc"`)
	mustContain(t, "pipeline/mk9frontpayload.sh", body, "rm -f $to")
	mustContain(t, "pipeline/mk9frontpayload.sh", body, "/rc/bin/ygg-peers")
}

func TestBuiltPayloadScriptsMatchSources(t *testing.T) {
	for src, dst := range map[string]string{
		"halatha.local": "../../../local/halatha-9front-payload/rc/bin/halatha.local",
		"ygg-up.rc":     "../../../local/halatha-9front-payload/rc/bin/ygg-up",
		"ygg-peers.rc":  "../../../local/halatha-9front-payload/rc/bin/ygg-peers",
	} {
		source := read(t, src)
		payload, err := os.ReadFile(filepath.Clean(dst))
		if err != nil {
			t.Skipf("payload not built yet: %v", err)
		}
		if string(payload) != source {
			t.Fatalf("payload script %s does not match source %s", dst, src)
		}
	}
}

// TestRcScriptsHaveNoBreakOrContinue guards a silent, expensive rc footgun.
// Plan 9 rc has no break/continue builtins: the line fails with "break: No
// such file or directory" and the loop runs to completion. In a
// wait-for-ready loop that means always paying the full timeout, which cost
// ~15s of boot time in halatha.local before this was fixed. Use a guard
// variable instead:
//
//	done=()
//	for(i in 1 2 3){
//		if(~ $#done 0){
//			if(cond)
//				done=1
//			if(~ $#done 0)
//				sleep 1
//		}
//	}
func TestRcScriptsHaveNoBreakOrContinue(t *testing.T) {
	bad := regexp.MustCompile(`(?m)^\s*(break|continue)\s*$`)
	for _, script := range []string{
		"halatha.local",
		"ygg-up.rc",
		"ygg-peers.rc",
		"clip.rc",
		"ipfs.rc",
		"pkg.rc",
		"termrc.local",
		"pipeline/halatha-grid-terminal.rc",
		"pipeline/install-halatha-http.rc",
	} {
		body := read(t, script)
		for i, line := range strings.Split(body, "\n") {
			if bad.MatchString(line) {
				t.Errorf("%s:%d uses rc break/continue, which does not exist: %s",
					script, i+1, strings.TrimSpace(line))
			}
		}
	}
}

// TestRcScriptsEndWithNewline guards the other classic rc footgun: a file
// with no trailing newline silently drops its last line or fails with
// "token EOF: syntax error".
func TestRcScriptsEndWithNewline(t *testing.T) {
	for _, script := range []string{
		"halatha.local",
		"ygg-up.rc",
		"ygg-peers.rc",
		"clip.rc",
		"ipfs.rc",
		"pkg.rc",
		"termrc.local",
		"pipeline/halatha-grid-terminal.rc",
		"pipeline/install-halatha-http.rc",
	} {
		body := read(t, script)
		if body == "" {
			t.Errorf("%s is empty", script)
			continue
		}
		if !strings.HasSuffix(body, "\n") {
			t.Errorf("%s does not end with a newline", script)
		}
	}
}

// The deployed 9legacy Rio rejects both empty and "none" attach names.
// A hidden window attachment gives the boot worker a real snarf endpoint.
// dev/test-clipboard-e2e.mjs checks both directions against the actual VM.
func TestClipSnarfNamespaceFallback(t *testing.T) {
	body := read(t, "clip.rc")

	for _, want := range []string{
		"rfork ne",
		"/mnt/wsys/snarf",
		"/srv/rio.",
		"new -hide -dx 200 -dy 150",
	} {
		mustContain(t, "clip.rc", body, want)
	}

	mustContain(t, "halatha.local", read(t, "halatha.local"), "rc /rc/bin/clip sync")
	mustNotContain(t, "clip.rc", body, "exec /rc/bin/clip.bin")
	// Plan 9 rm removes empty directories without a flag; -d is invalid.
	mustNotContain(t, "clip.rc", body, "rm -d ")
}

func TestClipSnapshotComparison(t *testing.T) {
	rc := plan9Rc(t)
	body := read(t, "clip.rc")
	start := strings.Index(body, "fn same_clip {")
	end := strings.Index(body, "fn sync_loop {")
	if start < 0 || end <= start {
		t.Fatal("missing snapshot comparator")
	}
	dir := t.TempDir()
	for name, content := range map[string]string{"empty": "", "full": "hello", "same": "hello", "prefix": "hell"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, pair := range []struct {
		a, b  string
		equal bool
	}{
		{"empty", "empty", true}, {"empty", "full", false},
		{"full", "empty", false}, {"full", "same", true},
		{"full", "prefix", false}, {"prefix", "full", false},
	} {
		cmd := exec.Command(rc, "-c", "compare=/usr/bin/cmp\n"+body[start:end]+"\nsame_clip "+pair.a+" "+pair.b)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if (err == nil) != pair.equal {
			t.Errorf("%s vs %s: %v %s", pair.a, pair.b, err, out)
		}
	}
}

// TestScriptsDoNotUseStatusAsAVariable guards a subtle rc footgun that made
// every `ipfs put/get/cat` and `pkg install` report the literal text
// "no match" instead of the real status.
//
// In rc, `status` is the builtin exit-status variable AND the ~ operator
// writes to it (rc/exec.c Xmatch calls setstatus("no match") before
// testing). So this destroys the value it is testing:
//
//	status=`{cat $root/status}
//	if(~ $status pinned* error*)   # $status is now "no match"
//
// It is clobbered on a successful match too, so the success path is equally
// wrong. Use any other name.
func TestScriptsDoNotUseStatusAsAVariable(t *testing.T) {
	// Setting `status` as a function's return code is idiomatic rc and fine.
	// The bug is READING it back after a ~ test, because ~ overwrites it. So
	// flag only `$status` uses, not plain assignment.
	// Go's regexp has no negative lookahead, so match $status followed by a
	// non-identifier char (or end of line) the long way.
	use := regexp.MustCompile(`\$status([^A-Za-z0-9_]|$)`)
	for _, script := range []string{
		"halatha.local",
		"halatha-bind.rc",
		"ygg-up.rc",
		"ygg-peers.rc",
		"clip.rc",
		"ipfs.rc",
		"pkg.rc",
	} {
		body := read(t, script)
		for i, line := range strings.Split(body, "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "#") {
				continue
			}
			if use.MatchString(line) {
				t.Errorf("%s:%d reads $status, which the ~ operator overwrites; use another name: %s",
					script, i+1, trimmed)
			}
		}
	}
}

// TestYggBinariesInstallIndependently guards a real failure seen in the
// guest: ygg-up gated BOTH installs on yggdrasil being absent, so once
// yggdrasil existed the whole block was skipped and yggdrasilctl was never
// fetched. ygg-peers then died with
// "ygg-peers: /386/bin/yggdrasilctl not present".
//
// The binaries are demand-loaded from the signed libtab tree rather than
// baked into the image (~18MB, most of the gzroot budget), so each one has
// to be checked and installed on its own.
func TestYggBinariesInstallIndependently(t *testing.T) {
	up := read(t, "ygg-up.rc")

	// The old shape: one condition covering both installs.
	if strings.Contains(up, "! test -x /386/bin/yggdrasil && test -x /rc/bin/pkg") {
		t.Error("ygg-up.rc gates both installs on yggdrasil alone; " +
			"yggdrasilctl will never be fetched once yggdrasil exists")
	}
	// It must consider both binaries.
	for _, b := range []string{"yggdrasil", "yggdrasilctl"} {
		if !strings.Contains(up, b) {
			t.Errorf("ygg-up.rc never mentions %q", b)
		}
	}
	mustContain(t, "ygg-up.rc", up, "pkg install")

	// ygg-peers must not simply fail when yggdrasilctl is missing: it can
	// pull it from libtab itself.
	peers := read(t, "ygg-peers.rc")
	mustContain(t, "ygg-peers.rc", peers, "pkg install yggdrasilctl")

	// The integration uses one known reachable peer, so a boot without a
	// bundled peer list can still join the overlay.
	mustContain(t, "ygg-peers.rc", peers, "PEER=tls://")
}

// TestYggConfPreservesNestedBraces guards a bug that made yggdrasil panic
// at startup. yggconf rewrites the generated config to inject our
// AdminListen, and it used to drop the opening brace with
//
//	grep -v '^[ \t]*[{][ \t]*$'
//
// That pattern matches ANY line holding only a brace -- including the
// nested `{` inside the MulticastInterfaces array. Its closing `}` was left
// behind, HJSON refused the unbalanced config, and cfg.ReadFrom panicked
// (cmd/yggdrasil/main.go:116). The daemon bound its admin socket, accepted
// 42 addPeer calls, then died -- which looked like a networking fault.
//
// Only the FIRST line is the real opening brace, so strip exactly that.
func TestYggConfPreservesNestedBraces(t *testing.T) {
	body := read(t, "ygg-up.rc")

	// Check code only -- the comment above the fix quotes the old pattern
	// on purpose, to explain what went wrong.
	for i, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		if strings.Contains(line, "[{][") {
			t.Errorf("ygg-up.rc:%d strips brace-only lines by pattern; this eats "+
				"the nested MulticastInterfaces brace and the config no longer parses", i+1)
		}
	}
	mustContain(t, "ygg-up.rc", body, "sed 1d")
}

// TestYggGuardIgnoresBrokenProcs executes ygg-up.rc's duplicate-daemon
// guard against a simulated `ps`, because the guard's failure mode is
// behavioural and invisible to a source grep.
//
// Plan 9 does not reap a process that dies on a note: pexit calls
// addbroken(up), which sets p->state = Broken (9/port/proc.c), and the
// corpse stays in the proc table until reboot. The original guard matched
// on the process NAME alone, so after yggdrasil's first panic it saw
// "yggdrasil.bin" in ps, concluded "already running", and refused to ever
// start a live daemon again -- leaving yggdrasilctl getPeers reporting
// every peer down with no daemon behind it.
//
// ps prints the state as argv[2] and the name as argv[0] (cmd/ps.c); the
// %-8.8s truncation there applies to the STATE field, not the name.
func TestYggGuardIgnoresBrokenProcs(t *testing.T) {
	rc := plan9Rc(t)
	guard := extractFn(t, read(t, "ygg-up.rc"), "ygglive")

	cases := []struct {
		name     string
		ps       []string
		wantLive bool
	}{
		{
			name:     "no yggdrasil at all",
			ps:       []string{"glenda 19 0:00 0:00 1024K Pread    rc"},
			wantLive: false,
		},
		{
			name:     "one live daemon",
			ps:       []string{"glenda 12 0:00 0:00 4096K Pread    yggdrasil.bin"},
			wantLive: true,
		},
		{
			// The regression: corpses only, nothing alive.
			name: "only broken corpses",
			ps: []string{
				"glenda 12 0:00 0:00 4096K Broken   yggdrasil.bin",
				"glenda 14 0:00 0:00 4096K Broken   yggdrasil.bin",
				"glenda 19 0:00 0:00 1024K Pread    rc",
			},
			wantLive: false,
		},
		{
			name: "corpses plus one live daemon",
			ps: []string{
				"glenda 12 0:00 0:00 4096K Broken   yggdrasil.bin",
				"glenda 21 0:00 0:00 4096K Sleep    yggdrasil.bin",
			},
			wantLive: true,
		},
		{
			// Name staged under a different basename must still count.
			name:     "plain yggdrasil name",
			ps:       []string{"glenda 12 0:00 0:00 4096K Pread    yggdrasil"},
			wantLive: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var fake strings.Builder
			fake.WriteString("fn ps {\n")
			for _, line := range tc.ps {
				fake.WriteString("\techo " + rcQuote(line) + "\n")
			}
			fake.WriteString("}\n")

			script := fake.String() + guard + "\n" +
				"if(ygglive) echo LIVE\n" +
				"if(! ygglive) echo DEAD\n"

			cmd := exec.Command(rc, "-c", script)
			var out bytes.Buffer
			cmd.Stdout = &out
			cmd.Stderr = &out
			_ = cmd.Run()
			got := out.String()

			live := strings.Contains(got, "LIVE")
			dead := strings.Contains(got, "DEAD")
			if live == dead {
				t.Fatalf("guard gave no clear verdict:\n%s", got)
			}
			if live != tc.wantLive {
				t.Errorf("guard said live=%v, want %v\nps was:\n%s\noutput:\n%s",
					live, tc.wantLive, strings.Join(tc.ps, "\n"), got)
			}
		})
	}
}

// TestYggUpReapsBrokenProcs pins the cleanup that keeps the guard honest.
// Writing "kill" to /proc/<pid>/ctl has an explicit `case Broken:
// unbreak(p)` branch (devproc.c CMkill); unbreak pulls the proc off the
// broken list and ready()s it so its pending Proc_exitme runs.
func TestYggUpReapsBrokenProcs(t *testing.T) {
	body := read(t, "ygg-up.rc")
	mustContain(t, "ygg-up.rc", body, "/proc/")
	mustContain(t, "ygg-up.rc", body, "echo kill >$d/ctl")

	// The status glob must key on BOTH the name and the Broken state, or
	// the reaper would kill a healthy daemon.
	if !strings.Contains(body, "*yggdrasi*Broken*") {
		t.Error("ygg-up.rc reaper must match name AND Broken state; " +
			"a name-only match would kill the live daemon")
	}
}

// extractFn pulls a single `fn name { ... }` block out of an rc script so a
// test can execute just that function against fixtures.
func extractFn(t *testing.T, body, name string) string {
	t.Helper()
	start := strings.Index(body, "fn "+name+" {")
	if start < 0 {
		t.Fatalf("no fn %s in script", name)
	}
	depth := 0
	for i := start; i < len(body); i++ {
		switch body[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return body[start : i+1]
			}
		}
	}
	t.Fatalf("unterminated fn %s", name)
	return ""
}

func rcQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

func plan9Rc(t *testing.T) string {
	t.Helper()
	candidates := []string{"/usr/local/plan9/bin/rc", "/opt/plan9/bin/rc"}
	if p := os.Getenv("PLAN9"); p != "" {
		candidates = append([]string{filepath.Join(p, "bin", "rc")}, candidates...)
	}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(home, "plan9", "bin", "rc"))
	}
	for _, candidate := range candidates {
		if st, err := os.Stat(candidate); err == nil && !st.IsDir() {
			return candidate
		}
	}
	if rc, err := exec.LookPath("rc"); err == nil {
		return rc
	}
	t.Skip("plan9port rc not found")
	return ""
}

func read(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(scriptRoot, filepath.Clean(name)))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func mustContain(t *testing.T, name, body, want string) {
	t.Helper()
	if !strings.Contains(body, want) {
		t.Fatalf("%s missing %q", name, want)
	}
}

func mustNotContain(t *testing.T, name, body, unwanted string) {
	t.Helper()
	if strings.Contains(body, unwanted) {
		t.Fatalf("%s contains fragile construct %q", name, unwanted)
	}
}
