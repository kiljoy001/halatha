// Command mkrelease builds the web root halatha serves: the page, the files it
// references, and the files release.manifest adds. Nothing else ships.
//
// The page is served as /index.html, so every reference resolves against the
// web root. A build fails, and writes nothing, when a referenced file is
// missing, when the page loads from a host the manifest does not allow, when a
// file that never belongs on a web server would ship (source, tests, notes,
// backups, private keys), or when a shipped file matches a pattern from the
// local forbidden-patterns file. That file is never committed, so the public
// source never names what it protects.
//
//	mkrelease -page examples/basic-v86-9front-direct.html -out out/release
package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

type Config struct {
	Root     string           // repository checkout
	Page     string           // page to serve as index.html, relative to Root
	Out      string           // output directory; must not exist or be empty
	Manifest []string         // release.manifest lines
	Patterns []*regexp.Regexp // content that must never ship
}

type Problem struct {
	Kind   string // missing, external, forbidden, content
	Path   string
	Detail string
}

type Report struct {
	Files    []string
	Problems []Problem
}

func (r *Report) failed() bool { return r != nil && len(r.Problems) > 0 }

func (r *Report) hasProblem(kind, p string) bool {
	if r == nil {
		return false
	}
	for _, pr := range r.Problems {
		if pr.Kind == kind && pr.Path == p {
			return true
		}
	}
	return false
}

func (r *Report) add(kind, p, detail string) {
	r.Problems = append(r.Problems, Problem{kind, p, detail})
}

func main() {
	root := flag.String("root", ".", "repository checkout")
	page := flag.String("page", "examples/basic-v86-9front-direct.html", "page served as index.html")
	out := flag.String("out", "out/release", "output directory (must not exist)")
	manifest := flag.String("manifest", "release.manifest", "extra paths the page loads indirectly")
	patterns := flag.String("patterns", defaultPatternsFile(), "local file of regexps that must never ship")
	flag.Parse()

	cfg := Config{Root: *root, Page: *page, Out: *out}
	if b, err := os.ReadFile(filepath.Join(*root, *manifest)); err == nil {
		cfg.Manifest = strings.Split(string(b), "\n")
	} else if !errors.Is(err, fs.ErrNotExist) {
		fatal(err)
	}
	pats, err := loadPatterns(*patterns)
	if err != nil {
		fatal(err)
	}
	if len(pats) == 0 {
		fmt.Fprintf(os.Stderr, "mkrelease: WARNING: no forbidden patterns loaded from %q; content is not checked\n", *patterns)
	}
	cfg.Patterns = pats

	rep, err := build(cfg)
	if err != nil {
		fatal(err)
	}
	if rep.failed() {
		sort.Slice(rep.Problems, func(i, j int) bool {
			a, b := rep.Problems[i], rep.Problems[j]
			return a.Kind+a.Path < b.Kind+b.Path
		})
		for _, p := range rep.Problems {
			fmt.Fprintf(os.Stderr, "%-9s %s  %s\n", p.Kind, p.Path, p.Detail)
		}
		fmt.Fprintf(os.Stderr, "mkrelease: %d problem(s); nothing written\n", len(rep.Problems))
		os.Exit(1)
	}
	fmt.Printf("mkrelease: %d files -> %s\n", len(rep.Files), *out)
}

func defaultPatternsFile() string {
	if p := os.Getenv("HALATHA_FORBIDDEN"); p != "" {
		return p
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "halatha", "forbidden")
}

// loadPatterns reads one regexp per line; blank lines and # comments are
// skipped. A missing file yields no patterns rather than an error, so a fresh
// checkout still builds, loudly.
func loadPatterns(file string) ([]*regexp.Regexp, error) {
	b, err := os.ReadFile(file)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []*regexp.Regexp
	for i, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		re, err := regexp.Compile(line)
		if err != nil {
			return nil, fmt.Errorf("%s:%d: %w", file, i+1, err)
		}
		out = append(out, re)
	}
	return out, nil
}

// parseManifest splits manifest lines into paths ("dir/" means the whole tree,
// globs are allowed) and allowed external URLs ("external <url>").
func parseManifest(text string) (paths, external []string) {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if rest, ok := strings.CutPrefix(line, "external "); ok {
			external = append(external, strings.TrimSpace(rest))
			continue
		}
		paths = append(paths, line)
	}
	return paths, external
}

var (
	attrRef   = regexp.MustCompile(`(?i)\b(?:src|href|data-src)\s*=\s*(?:"([^"]*)"|'([^']*)')`)
	stringLit = regexp.MustCompile("\"([^\"\\n]*)\"|'([^'\\n]*)'|`([^`]*)`")
	scheme    = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9+.-]*:`)
)

// extractRefs finds the references a page makes to served files: every
// src/href attribute, and every string literal that resolves under a
// top-level directory of the checkout or names another host. In-guest paths
// such as "boot/x" or "/n/host/y" do not resolve under a checkout directory,
// so seed-table destinations are not demanded from the web root. Template
// literals with ${} are skipped: their value exists only at run time.
func extractRefs(page string, top map[string]bool) []string {
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	for _, m := range attrRef.FindAllStringSubmatch(page, -1) {
		add(m[1] + m[2])
	}
	for _, m := range stringLit.FindAllStringSubmatch(page, -1) {
		s := m[1] + m[2] + m[3]
		if s == "" || strings.ContainsAny(s, " \t\n") || strings.Contains(s, "${") || !strings.Contains(s, "/") {
			continue
		}
		p, ext, ok := resolveRef(s)
		if !ok {
			continue
		}
		if ext {
			if strings.HasPrefix(s, "//") || strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") {
				add(s)
			}
			continue
		}
		if first, _, _ := strings.Cut(p, "/"); top[first] {
			add(s)
		}
	}
	return out
}

// resolveRef turns a reference into a path relative to the web root. External
// references come back unchanged with external set. Fragments, data: and other
// non-file schemes, and directory prefixes ("../local/") are not files: ok is
// false. ".." cannot climb above the root, exactly as in a browser.
func resolveRef(raw string) (p string, external, ok bool) {
	raw = strings.TrimSpace(raw)
	switch {
	case raw == "", strings.HasPrefix(raw, "#"):
		return "", false, false
	case strings.HasPrefix(raw, "//"), strings.HasPrefix(raw, "http://"), strings.HasPrefix(raw, "https://"),
		strings.HasPrefix(raw, "ws://"), strings.HasPrefix(raw, "wss://"):
		return raw, true, true
	case scheme.MatchString(raw):
		return "", false, false
	}
	if i := strings.IndexAny(raw, "?#"); i >= 0 {
		raw = raw[:i]
	}
	if raw == "" || strings.HasSuffix(raw, "/") {
		return "", false, false
	}
	p = strings.TrimPrefix(path.Clean("/"+raw), "/")
	if p == "" || p == "." {
		return "", false, false
	}
	return p, false, true
}

// forbiddenName reports files that never belong on a public web server.
func forbiddenName(p string) (why string, bad bool) {
	for _, seg := range strings.Split(p, "/") {
		if seg == ".git" || seg == "node_modules" {
			return "repository internals", true
		}
	}
	base := path.Base(p)
	switch base {
	case "Makefile", "makefile", "GNUmakefile", "Dockerfile", ".gitignore", ".env":
		return "build or repository file", true
	}
	switch {
	case strings.HasSuffix(base, ".go"):
		return "source", true
	case strings.HasSuffix(base, ".md"):
		return "notes", true
	case strings.HasSuffix(base, ".sh"):
		return "script", true
	case strings.HasSuffix(base, ".priv"), strings.HasSuffix(base, ".key"):
		return "private key", true
	case strings.Contains(base, ".bak"), strings.Contains(base, ".before-"),
		strings.HasSuffix(base, ".orig"), strings.HasSuffix(base, "~"), strings.HasSuffix(base, ".swp"):
		return "backup", true
	}
	return "", false
}

// build collects the release, validates all of it, and only then writes it.
func build(cfg Config) (*Report, error) {
	rep := &Report{}
	if entries, err := os.ReadDir(cfg.Out); err == nil && len(entries) > 0 {
		return rep, fmt.Errorf("output %s is not empty; refusing to overwrite", cfg.Out)
	}
	pageBytes, err := os.ReadFile(filepath.Join(cfg.Root, cfg.Page))
	if err != nil {
		return rep, err
	}
	top, err := topDirs(cfg.Root)
	if err != nil {
		return rep, err
	}
	paths, allowed := parseManifest(strings.Join(cfg.Manifest, "\n"))
	allowedExt := map[string]bool{}
	for _, u := range allowed {
		allowedExt[u] = true
	}

	files := map[string]string{"index.html": filepath.Join(cfg.Root, cfg.Page)} // release path -> source
	need := func(p, why string) {
		src := filepath.Join(cfg.Root, filepath.FromSlash(p))
		if st, err := os.Stat(src); err != nil || st.IsDir() {
			rep.add("missing", p, why)
			return
		}
		files[p] = src
	}
	for _, raw := range extractRefs(string(pageBytes), top) {
		p, ext, ok := resolveRef(raw)
		switch {
		case !ok:
		case ext:
			if !allowedExt[p] {
				rep.add("external", p, "page loads from another host; allow it with 'external "+p+"'")
			}
		default:
			need(p, "referenced by "+cfg.Page)
		}
	}
	for _, entry := range paths {
		if err := expandManifest(cfg.Root, entry, rep, need); err != nil {
			return rep, err
		}
	}

	for p, src := range files {
		if why, bad := forbiddenName(p); bad {
			rep.add("forbidden", p, why)
			continue
		}
		if len(cfg.Patterns) > 0 {
			if hit, err := matchFile(src, cfg.Patterns); err != nil {
				return rep, err
			} else if hit != "" {
				rep.add("content", p, "matches forbidden pattern "+hit)
			}
		}
	}
	if rep.failed() {
		return rep, nil
	}

	for p := range files {
		rep.Files = append(rep.Files, p)
	}
	sort.Strings(rep.Files)

	// Copy into a sibling and rename it into place, so an I/O failure part
	// way through never leaves a partial release where a deploy could find it.
	if err := os.MkdirAll(filepath.Dir(cfg.Out), 0o755); err != nil {
		return rep, err
	}
	tmp, err := os.MkdirTemp(filepath.Dir(cfg.Out), ".mkrelease-*")
	if err != nil {
		return rep, err
	}
	for _, p := range rep.Files {
		if err := copyFile(files[p], filepath.Join(tmp, filepath.FromSlash(p))); err != nil {
			os.RemoveAll(tmp)
			return rep, err
		}
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		os.RemoveAll(tmp)
		return rep, err
	}
	if err := os.Rename(tmp, cfg.Out); err != nil {
		os.RemoveAll(tmp)
		return rep, err
	}
	return rep, nil
}

func expandManifest(root, entry string, rep *Report, need func(p, why string)) error {
	why := "listed in release.manifest"
	if dir, ok := strings.CutSuffix(entry, "/"); ok {
		base := filepath.Join(root, filepath.FromSlash(dir))
		if st, err := os.Stat(base); err != nil || !st.IsDir() {
			rep.add("missing", entry, why)
			return nil
		}
		return filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			rel, _ := filepath.Rel(root, p)
			need(filepath.ToSlash(rel), why)
			return nil
		})
	}
	if strings.ContainsAny(entry, "*?[") {
		matches, err := filepath.Glob(filepath.Join(root, filepath.FromSlash(entry)))
		if err != nil {
			return err
		}
		if len(matches) == 0 {
			rep.add("missing", entry, why+" (glob matched nothing)")
		}
		for _, m := range matches {
			rel, _ := filepath.Rel(root, m)
			need(filepath.ToSlash(rel), why)
		}
		return nil
	}
	need(entry, why)
	return nil
}

func topDirs(root string) (map[string]bool, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	top := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() {
			top[e.Name()] = true
		}
	}
	return top, nil
}

// matchFile streams the file past each pattern, so multi-gigabyte disk
// images are checked without being held in memory.
func matchFile(file string, pats []*regexp.Regexp) (string, error) {
	for _, re := range pats {
		f, err := os.Open(file)
		if err != nil {
			return "", err
		}
		hit := re.MatchReader(bufio.NewReaderSize(f, 1<<20))
		f.Close()
		if hit {
			return re.String(), nil
		}
	}
	return "", nil
}

func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "mkrelease:", err)
	os.Exit(1)
}
