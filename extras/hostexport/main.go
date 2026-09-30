package main

import (
	"log"
	"os"

	"go.bug.st/serial"
	"tractor.dev/wanix"
	"tractor.dev/wanix/fs/localfs"
	"tractor.dev/wanix/fs/net/tcp"
	"tractor.dev/wanix/fs/p9p"
	"tractor.dev/wanix/native"
	"tractor.dev/wanix/term"
	"tractor.dev/wanix/wisp"
)

func main() {
	log.SetFlags(log.Lshortfile)

	taskfs := wanix.NewTaskFS()
	taskfs.Register("native", &native.ExecDriver{})
	taskfs.Register("wisp", &wisp.Driver{})
	root, err := wanix.NewRootWithTasks(taskfs)
	if err != nil {
		log.Fatal(err)
	}

	lfsys, err := localfs.New("/")
	if err != nil {
		log.Fatal(err)
	}
	if err := root.NS().Bind(lfsys, ".", "."); err != nil {
		log.Fatal(err)
	}

	termfs := term.New(nil)
	if err := root.NS().Bind(termfs, ".", "#term"); err != nil {
		log.Fatal(err)
	}

	// Bind the previously-unused Plan 9-style /net/tcp service so the native
	// kernel namespace has a real-TCP capability (net/tcp). The wisp task kind
	// (registered above) lets a guest or host script start a WISP relay via
	// #task/new/wisp + start.
	if err := root.NS().Bind(tcp.New(), ".", "net/tcp"); err != nil {
		log.Fatal("bind net/tcp:", err)
	}

	if err := root.Bind("#task", "task"); err != nil {
		log.Fatal(err)
	}
	if err := root.Bind("#term", "term"); err != nil {
		log.Fatal(err)
	}

	exportdev := os.Getenv("EXPORTDEV")
	if exportdev == "" {
		if err := p9p.Serve(root.NS(), os.Stdin, os.Stdout); err != nil {
			log.Fatal(err)
		}
		return
	}

	mode := &serial.Mode{
		BaudRate: 115200,
	}
	dev, err := serial.Open(exportdev, mode)
	if err != nil {
		log.Fatal(err)
	}
	defer dev.Close()

	// first packet signals to start client export
	if _, err := dev.Write([]byte("!")); err != nil {
		log.Fatal(err)
	}

	if err := p9p.Serve(root.NS(), dev, dev); err != nil {
		log.Fatal(err)
	}
}
