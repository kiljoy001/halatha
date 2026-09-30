// Direct 9P acceptance checks run outside the browser gateway. Run this on each
// approved/unapproved Yggdrasil node; an HTTP handshake cannot influence it.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"strings"
	"time"

	"9fans.net/go/plan9"
	"9fans.net/go/plan9/client"
)

type targets []string

func (t *targets) String() string { return strings.Join(*t, ",") }
func (t *targets) Set(s string) error {
	if _, _, err := net.SplitHostPort(s); err != nil {
		return err
	}
	*t = append(*t, s)
	return nil
}

func main() {
	var allow, denied targets
	flag.Var(&allow, "allow", "endpoint that must permit a read (repeatable)")
	flag.Var(&denied, "deny", "endpoint that must reject a direct anonymous mount (repeatable)")
	read := flag.String("read", "", "public relative file to read through approved exports")
	aname := flag.String("aname", "", "fixed export attach name")
	expected := flag.String("sha256", "", "expected file hash")
	readonly := flag.Bool("read-only", true, "require server to reject write-only opens; never write or truncate")
	timeout := flag.Duration("timeout", 5*time.Second, "deadline for each direct mount")
	flag.Parse()
	if len(allow) == 0 && len(denied) == 0 {
		fmt.Fprintln(os.Stderr, "supply --allow and/or --deny endpoints")
		os.Exit(2)
	}
	if *timeout <= 0 {
		fmt.Fprintln(os.Stderr, "--timeout must be positive")
		os.Exit(2)
	}
	if len(allow) > 0 && (!fs.ValidPath(*read) || *read == ".") {
		fmt.Fprintln(os.Stderr, "allowed endpoints require a public relative --read path and positive timeout")
		os.Exit(2)
	}
	if *expected != "" {
		hash, err := hex.DecodeString(*expected)
		if err != nil || len(hash) != 32 {
			fmt.Fprintln(os.Stderr, "--sha256 must contain 32 hex bytes")
			os.Exit(2)
		}
	}
	// Positive controls run first: denial is not mistaken for a complete outage.
	for _, endpoint := range allow {
		conn, mounted, err := mount(endpoint, *aname, *timeout)
		if err != nil {
			fail("approved endpoint %s: %v", endpoint, err)
		}
		file, err := mounted.Open(*read, plan9.OREAD)
		if err != nil {
			conn.Close()
			fail("read public file at %s: %v", endpoint, err)
		}
		hash := sha256.New()
		size, err := io.Copy(hash, file)
		file.Close()
		if err != nil {
			conn.Close()
			fail("read at %s: %v", endpoint, err)
		}
		digest := hex.EncodeToString(hash.Sum(nil))
		if *expected != "" && !strings.EqualFold(*expected, digest) {
			conn.Close()
			fail("hash mismatch at %s", endpoint)
		}
		if *readonly {
			writable, err := mounted.Open(*read, plan9.OWRITE)
			if err == nil {
				writable.Close()
				conn.Close()
				fail("server allows writable opens at %s; no writes were attempted", endpoint)
			}
			// Distinguish a write denial from a dropped or broken connection.
			check, err := mounted.Open(*read, plan9.OREAD)
			if err != nil {
				conn.Close()
				fail("read-only control after denied write open at %s: %v", endpoint, err)
			}
			check.Close()
		}
		conn.Close()
		fmt.Printf("PASS approved direct 9P access %s: bytes=%d sha256=%s readonly=%v\n", endpoint, size, digest, *readonly)
	}
	for _, endpoint := range denied {
		conn, _, err := mount(endpoint, *aname, *timeout)
		if err == nil {
			conn.Close()
			fail("forbidden endpoint %s accepted an anonymous mount", endpoint)
		}
		fmt.Printf("PASS direct mount denied at %s\n", endpoint)
	}
	if len(allow) == 0 {
		fmt.Println("NOTE denial-only run needs a separate successful approved-node control to exclude an outage")
	}
}

func mount(endpoint, aname string, timeout time.Duration) (net.Conn, *client.Fsys, error) {
	conn, err := net.DialTimeout("tcp", endpoint, timeout)
	if err != nil {
		return nil, nil, err
	}
	conn.SetDeadline(time.Now().Add(timeout))
	c, err := client.NewConn(conn)
	if err != nil {
		conn.Close()
		return nil, nil, err
	}
	mounted, err := c.Attach(nil, "none", aname)
	if err != nil {
		conn.Close()
		return nil, nil, err
	}
	return conn, mounted, nil
}
func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "FAIL "+format+"\n", args...)
	os.Exit(1)
}
