// Command 9pmount resolves a user-space 9P locator and mounts it.
//
//	9pmount '9p+il://[200::1]!564/' /n/remote
//	9pmount -u /n/remote
//
// The kernel never parses a URI. This helper turns one into a Plan 9
// dial string (or a kernel channel) and drives srv(1)/mount(1):
//
//	il/tcp  srv '<transport>!host!port' <name>
//	        mount [-n] /srv/<name> <mountpoint> [aname]
//	tag     mount -c '#9/<tag>' <mountpoint> [aname]
//	ring    mount -c '#~/<n>' <mountpoint> [aname]
//
// Build for a 9front guest with:
//
//	GOOS=plan9 GOARCH=386 CGO_ENABLED=0 go build -o 9pmount.386 ./cmd/9pmount
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"

	"tractor.dev/wanix/internal/ninepuri"
)

func usage() {
	fmt.Fprintf(os.Stderr, `usage: 9pmount [flags] URI MOUNTPOINT
       9pmount -u MOUNTPOINT
       9pmount -dry-run URI MOUNTPOINT

Schemes:
  9p://host!port/aname        tcp, default port 564
  9p+tcp://host!port/aname    tcp
  9p+il://[200::1]!564/aname  IL over the local IP stack (incl. Yggdrasil)
  9p+tag://host9p/aname        virtio-9p tag channel (#9/host9p)
  9p+ring://0/aname            memory-ring channel (#~/0)

Flags:
`)
	flag.PrintDefaults()
}

func main() {
	unmount := flag.Bool("u", false, "unmount the mountpoint")
	dry := flag.Bool("dry-run", false, "print the srv/mount commands without running them")
	svcName := flag.String("name", "", "service name under /srv (default: URI-derived + pid)")
	aname := flag.String("aname", "", "override the 9P attach name")
	verbose := flag.Bool("v", false, "report the resolved transport")
	flag.Usage = usage
	flag.Parse()

	if flag.NArg() != 2 {
		usage()
		os.Exit(2)
	}
	u, err := ninepuri.Parse(flag.Arg(0))
	if err != nil {
		fatal(err)
	}
	mnt := flag.Arg(1)

	if *unmount {
		run(*dry, "/bin/unmount", mnt)
		return
	}
	if *aname != "" {
		u.Aname = *aname
	}

	switch u.Transport {
	case ninepuri.TransportIL, ninepuri.TransportTCP:
		svc := *svcName
		if svc == "" {
			svc = fmt.Sprintf("%s-%d", u.ServiceName(), os.Getpid())
		}
		if *verbose {
			fmt.Fprintf(os.Stderr, "9pmount: %s -> %s (srv %s)\n", u.Raw, u.DialString(), svc)
		}
		// Two arguments: post the connection under /srv/<svc> without
		// mounting. mount(1) below supplies the attach name.
		run(*dry, "/bin/srv", u.DialString(), svc)
		args := []string{}
		if u.NoAuth() {
			args = append(args, "-n")
		}
		args = append(args, "/srv/"+svc, mnt)
		if u.Aname != "" {
			args = append(args, u.Aname)
		}
		run(*dry, "/bin/mount", args...)
	case ninepuri.TransportTag, ninepuri.TransportRing:
		args := []string{"-c"}
		if u.NoAuth() {
			args = append(args, "-n")
		}
		args = append(args, u.DialString(), mnt)
		if u.Aname != "" {
			args = append(args, u.Aname)
		}
		run(*dry, "/bin/mount", args...)
	default:
		fatal(fmt.Errorf("unsupported transport %q", u.Transport))
	}
}

func run(dry bool, cmd string, args ...string) {
	if dry {
		fmt.Printf("%s %s\n", cmd, join(args))
		return
	}
	c := exec.Command(cmd, args...)
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	if err := c.Run(); err != nil {
		fatal(fmt.Errorf("%s %s: %w", cmd, join(args), err))
	}
}

func join(args []string) string {
	out := ""
	for i, a := range args {
		if i > 0 {
			out += " "
		}
		out += a
	}
	return out
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "9pmount:", err)
	os.Exit(1)
}
