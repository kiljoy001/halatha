//go:build !js && !wasm

package main

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"testing/quick"
)

// The page is served as /index.html, so every reference resolves against the
// web root. "../v86/x" from the root is "/v86/x": a browser clamps ".." at the
// root, and the release has to put the file where the browser will look.
func TestResolveRef(t *testing.T) {
	cases := []struct {
		raw, want string
		external  bool
		ok        bool
	}{
		{"../v86/bundle/v86.wasm", "v86/bundle/v86.wasm", false, true},
		{"/dist/wanix.min.js", "dist/wanix.min.js", false, true},
		{"extras/9front/ygg-up.rc", "extras/9front/ygg-up.rc", false, true},
		{"./local/a.img?x=1#frag", "local/a.img", false, true},
		{"../../../etc/passwd", "etc/passwd", false, true},
		{"//i.copy.sh/9front_state-v3.bin", "//i.copy.sh/9front_state-v3.bin", true, true},
		{"https://example.com/a.js", "https://example.com/a.js", true, true},
		{"#top", "", false, false},
		{"data:text/plain,hi", "", false, false},
		{"mailto:a@b", "", false, false},
		{"/", "", false, false},
	}
	for _, c := range cases {
		got, ext, ok := resolveRef(c.raw)
		if ok != c.ok || (ok && (got != c.want || ext != c.external)) {
			t.Errorf("resolveRef(%q) = %q, external=%v, ok=%v; want %q, %v, %v",
				c.raw, got, ext, ok, c.want, c.external, c.ok)
		}
	}
}

// Seed tables pair a served source with an in-guest destination:
// ["extras/9front/ygg-up.rc", "boot/ygg-up.rc"]. The source must ship; the
// destination is a guest path and must not be demanded from the web root.
// Template literals are skipped because their value is only known at run time.
func TestExtractRefs(t *testing.T) {
	page := `<script type="module" src="/dist/wanix.min.js"></script>
<link href="style.css" rel="stylesheet">
<script>
const scripts = [
    ["extras/9front/ygg-up.rc", "boot/ygg-up.rc"],
    ['extras/halatha/keyrequest/client.o9', 'boot/keyrequest-client.o9'],
];
const bios = { url: "../v86/bundle/seabios.bin" };
const state = "//i.copy.sh/9front_state-v3.bin";
fetch(` + "`/${src}`" + `);
const tmpl = ` + "`extras/9front/${name}.rc`" + `;
const word = "hello/world";
</script>`
	top := map[string]bool{"extras": true, "dist": true, "v86": true}
	got := extractRefs(page, top)
	want := []string{
		"../v86/bundle/seabios.bin",
		"//i.copy.sh/9front_state-v3.bin",
		"/dist/wanix.min.js",
		"extras/9front/ygg-up.rc",
		"extras/halatha/keyrequest/client.o9",
		"style.css",
	}
	sort.Strings(got)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("extractRefs:\n got %q\nwant %q", got, want)
	}
}

// These never belong on a public web server: source and tests, build files,
// notes, editor and deploy backups, and above all private keys.
func TestForbiddenName(t *testing.T) {
	bad := []string{
		"cmd/wanix/serve.go", "extras/9front/script_test/ygg_scripts_test.go",
		"extras/deployment/README.md", "Makefile", "extras/9front/mk.sh",
		"extras/9legacy/halatha-root.priv", "index.html.bak-pre-sensor",
		"extras/9front/ygg-up.rc.bak-20260923-brokenguard", "a/.gitignore",
		".git/config", "node_modules/x/index.js", "index.before-keyrequest.html",
		"local/9legacy-wanix-virtio9p.img.bak-prebind", "a.orig", "a~",
	}
	for _, p := range bad {
		if _, isBad := forbiddenName(p); !isBad {
			t.Errorf("forbiddenName(%q) allowed it", p)
		}
	}
	good := []string{
		"index.html", "dist/wanix.min.js", "dist/wanix.wasm", "extras/9front/ygg-up.rc",
		"extras/9legacy/bin.tab", "extras/9legacy/halatha-root.pub.hex",
		"extras/halatha/keyrequest/client.o9", "v86/bundle/v86.wasm", "local/9front-halatha.img",
	}
	for _, p := range good {
		if why, isBad := forbiddenName(p); isBad {
			t.Errorf("forbiddenName(%q) rejected it: %s", p, why)
		}
	}
}

// Property: whatever the manifest asks for, a forbidden file never reaches the
// output. The manifest is edited by hand, so it cannot be the safety net.
func TestForbiddenNeverShips(t *testing.T) {
	suffixes := []string{".go", "_test.go", ".md", ".priv", ".sh", ".bak", ".orig"}
	f := func(name uint16, pick uint8) bool {
		root := t.TempDir()
		bad := filepath.Join("extras", "p", strings.Repeat("x", int(name%7)+1)+suffixes[int(pick)%len(suffixes)])
		write(t, root, "index.html", "<p>hi</p>")
		write(t, root, bad, "secret")
		out := filepath.Join(t.TempDir(), "out")
		rep, err := build(Config{Root: root, Page: "index.html", Out: out, Manifest: []string{filepath.ToSlash(bad)}})
		if err == nil && !rep.failed() {
			return false
		}
		if !rep.hasProblem("forbidden", filepath.ToSlash(bad)) {
			return false
		}
		entries, _ := os.ReadDir(out)
		return len(entries) == 0
	}
	if err := quick.Check(f, &quick.Config{MaxCount: 40}); err != nil {
		t.Error(err)
	}
}

// A build ships exactly the page, what it references, and what the manifest
// adds; directory entries ("dir/") pull in the whole tree.
func TestBuildShipsExactlyWhatIsUsed(t *testing.T) {
	root := t.TempDir()
	write(t, root, "examples/page.html", `<script src="/dist/w.js"></script>
<script>const s = [["extras/9front/a.rc", "boot/a.rc"]]; const b = "../v86/bios.bin";</script>`)
	write(t, root, "dist/w.js", "js")
	write(t, root, "dist/unused.js", "js")
	write(t, root, "extras/9front/a.rc", "rc")
	write(t, root, "extras/9front/b.rc", "rc")
	write(t, root, "v86/bios.bin", "bios")
	write(t, root, "extras/9legacy/bin/386/cat", "elf")
	write(t, root, "extras/9legacy/bin/386/ls", "elf")
	write(t, root, "local/disk.img", "disk")

	out := filepath.Join(t.TempDir(), "out")
	rep, err := build(Config{
		Root: root, Page: "examples/page.html", Out: out,
		Manifest: []string{"extras/9legacy/bin/", "local/disk.img"},
	})
	if err != nil || rep.failed() {
		t.Fatalf("build failed: %v %v", err, rep.Problems)
	}
	want := []string{
		"dist/w.js", "extras/9front/a.rc", "extras/9legacy/bin/386/cat",
		"extras/9legacy/bin/386/ls", "index.html", "local/disk.img", "v86/bios.bin",
	}
	if got := listFiles(t, out); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("shipped:\n got %q\nwant %q", got, want)
	}
}

// A referenced file that does not exist is a broken page, not a smaller one.
// It must be reported as a problem before anything is written: an I/O error
// halfway through copying would leave a partial release behind.
func TestBuildFailsOnMissingRef(t *testing.T) {
	root := t.TempDir()
	write(t, root, "index.html", `<script src="/dist/gone.js"></script>`)
	out := filepath.Join(t.TempDir(), "out")
	rep, err := build(Config{Root: root, Page: "index.html", Out: out})
	if err != nil || !rep.hasProblem("missing", "dist/gone.js") {
		t.Errorf("missing reference not reported as a problem: err=%v problems=%v", err, rep.Problems)
	}
	assertNothingWritten(t, out)
}

// A third-party host serving part of the VM is a dependency nobody reviewed;
// it has to be allowed by name in the manifest.
func TestBuildFailsOnUnlistedExternal(t *testing.T) {
	root := t.TempDir()
	write(t, root, "index.html", `<script>const s = "//i.copy.sh/9front_state-v3.bin";</script>`)
	out := filepath.Join(t.TempDir(), "out")
	rep, err := build(Config{Root: root, Page: "index.html", Out: out})
	if err != nil || !rep.hasProblem("external", "//i.copy.sh/9front_state-v3.bin") {
		t.Errorf("unlisted external not reported as a problem: err=%v problems=%v", err, rep.Problems)
	}
	assertNothingWritten(t, out)
	rep, err = build(Config{Root: root, Page: "index.html", Out: out,
		Manifest: []string{"external //i.copy.sh/9front_state-v3.bin"}})
	if err != nil || rep.failed() {
		t.Errorf("allowed external still failed: %v %v", err, rep.Problems)
	}
}

// Home identifiers come from a local file that is never committed, so the
// public source never names what it is protecting.
func TestBuildFailsOnForbiddenContent(t *testing.T) {
	root := t.TempDir()
	write(t, root, "index.html", `<script src="/extras/a.rc"></script>`)
	write(t, root, "extras/a.rc", "dns=[201:aaaa:bbbb::1]:53\n")
	pat := regexp.MustCompile(`201:aaaa:bbbb`)
	out := filepath.Join(t.TempDir(), "out")
	rep, err := build(Config{Root: root, Page: "index.html", Out: out, Patterns: []*regexp.Regexp{pat}})
	if err != nil || !rep.hasProblem("content", "extras/a.rc") {
		t.Errorf("forbidden content not reported as a problem: err=%v problems=%v", err, rep.Problems)
	}
	assertNothingWritten(t, out)
}

// An existing release is never overwritten or merged into: stale files from
// an older build would ship alongside the new one.
func TestBuildRefusesExistingOutput(t *testing.T) {
	root := t.TempDir()
	write(t, root, "index.html", "<p>hi</p>")
	out := t.TempDir()
	write(t, out, "old.txt", "previous release")
	if _, err := build(Config{Root: root, Page: "index.html", Out: out}); err == nil {
		t.Errorf("build into a non-empty output succeeded")
	}
	if got := listFiles(t, out); strings.Join(got, ",") != "old.txt" {
		t.Errorf("existing output was modified: %q", got)
	}
}

// A failed build leaves no output at all, so a half-built release can never
// be deployed by mistake.
func assertNothingWritten(t *testing.T, out string) {
	t.Helper()
	if entries, err := os.ReadDir(out); err == nil && len(entries) > 0 {
		t.Errorf("failed build wrote %d entries to %s", len(entries), out)
	}
}

func TestParseManifest(t *testing.T) {
	got, ext := parseManifest("# comment\n\nextras/9legacy/bin/\n  local/a.img  \nexternal //cdn.example/x.js\n")
	if strings.Join(got, ",") != "extras/9legacy/bin/,local/a.img" {
		t.Errorf("paths = %q", got)
	}
	if len(ext) != 1 || ext[0] != "//cdn.example/x.js" {
		t.Errorf("external = %q", ext)
	}
}

// Whatever text a page holds, extracted references stay inside the web root
// once resolved: no absolute filesystem paths and no escaping with "..".
func FuzzResolvedRefsStayInRoot(f *testing.F) {
	f.Add(`<script src="../../x.js"></script>["extras/a", "boot/a"]`)
	f.Add("`/${src}` '../../../etc/passwd' \"//host/p\" \"/\"")
	top := map[string]bool{"extras": true, "etc": true}
	f.Fuzz(func(t *testing.T, page string) {
		for _, raw := range extractRefs(page, top) {
			p, ext, ok := resolveRef(raw)
			if !ok || ext {
				continue
			}
			if p == "" || strings.HasPrefix(p, "/") || p == ".." || strings.HasPrefix(p, "../") || strings.Contains(p, "/../") {
				t.Fatalf("resolveRef(%q) escaped the root: %q", raw, p)
			}
		}
	})
}

func write(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func listFiles(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(root, p)
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	sort.Strings(out)
	return out
}
