package api

import (
	"log"

	"tractor.dev/toolkit-go/duplex/rpc"
	"tractor.dev/wanix/fs"
)

func (s *syscaller) readDir(r rpc.Responder, c *rpc.Call) {
	var args []string
	c.Receive(&args)

	// log.Println("ReadDir", args)

	dir, err := fs.ReadDir(s.task.NS(), args[0])
	if err != nil {
		log.Println("err:", args[0], err)
		r.Return(err)
		return
	}

	// Start with an empty (non-nil) slice so an empty directory marshals
	// to a JS array `[]` rather than `null`; callers iterate the result.
	entries := []string{}
	for _, e := range dir {
		name := e.Name()
		if e.IsDir() {
			name = name + "/"
		}
		entries = append(entries, name)
	}

	r.Return(entries)
}
