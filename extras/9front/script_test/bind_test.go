package script_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestHalathaBindWaitsForVerificationVerdict(t *testing.T) {
	rc := plan9Rc(t)
	for _, tc := range []struct {
		name     string
		statuses []string
		binds    bool
		message  string
	}{
		{"eventual success", []string{"initializing", "VERIFYING", "VERIFIED: 3 binaries"}, true, ""},
		{"rejection", []string{"initializing", "REJECTED: bad signature"}, false, "refusing unverified"},
		{"timeout", []string{"VERIFYING"}, false, "timed out waiting for verification"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			statusFile := filepath.Join(dir, "status")
			if err := os.WriteFile(statusFile, []byte(tc.statuses[0]), 0600); err != nil {
				t.Fatal(err)
			}
			// sleep is replaced with status transitions; no wall-clock delays.
			// A separate rc sleep shim consumes one status per invocation.
			sequence := filepath.Join(dir, "sequence")
			script := "#!/bin/sh\n" + "n=0\n[ ! -f '" + sequence + "' ] || n=$(cat '" + sequence + "')\nn=$((n+1))\necho $n > '" + sequence + "'\ncase $n in\n"
			for i, status := range tc.statuses[1:] {
				script += strings.Join([]string{fmt.Sprint(i + 1), ") printf '%s' '", status, "' > '", statusFile, "';;\n"}, "")
			}
			script += "esac\n"
			shim := filepath.Join(dir, "sleep")
			if err := os.WriteFile(shim, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			probe := `fn test { ~ $1 -d -f }
   fn cat { /bin/cat ` + statusFile + ` }
   fn sleep { ` + shim + ` }
   fn bind { echo BOUND }
   . ../halatha-bind.rc
   `
			out, err := exec.Command(rc, "-c", probe).CombinedOutput()
			if strings.Contains(string(out), "syntax error") {
				t.Fatalf("rc parse: %s", out)
			}
			if strings.Contains(string(out), "BOUND") != tc.binds {
				t.Fatalf("bind=%v: %v %s", tc.binds, err, out)
			}
			if tc.message != "" && !strings.Contains(string(out), tc.message) {
				t.Fatalf("missing %q: %s", tc.message, out)
			}
		})
	}
}
