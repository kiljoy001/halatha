//go:build js && wasm

package gojs

import (
	"log"
	"syscall/js"

	"tractor.dev/wanix/fs"
	"tractor.dev/wanix/fs/p9p"
	"tractor.dev/wanix/misc/jsutil"
)

func Export(fsys fs.FS, debug bool) error {
	msgch := js.Global().Get("MessageChannel").New()
	js.Global().Get("self").Call("postMessage", map[string]any{
		"export": msgch.Get("port2"),
	}, []any{msgch.Get("port2")})

	rwc := jsutil.NewPortReadWriter(msgch.Get("port1"))
	go func() {
		if debug {
			log.Println("gojs export: 9P2000 session started")
		}
		if err := p9p.Serve(fsys, rwc, rwc); err != nil {
			log.Fatal(err)
		}
	}()
	msgch.Get("port1").Call("postMessage", "!") // signal to worker that export is ready
	return nil
}
