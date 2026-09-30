package p9p

import (
	"context"
	"testing"

	"github.com/knusbaum/go9p/proto"
	"tractor.dev/wanix/fs"
	"tractor.dev/wanix/fs/memfs"
	"tractor.dev/wanix/fs/vfs"
)

func TestClipboardTruncateThroughNamespace(t *testing.T) {
	for _, replacement := range []string{"short", ""} {
		t.Run(replacement, func(t *testing.T) {
			memory := memfs.New()
			ns := vfs.New(context.Background())
			if err := ns.Bind(memory, ".", "clip"); err != nil {
				t.Fatal(err)
			}
			if err := fs.WriteFile(ns, "clip/text", []byte("previous long clipboard"), 0666); err != nil {
				t.Fatal(err)
			}
			s := &srv{Fsys: ns}
			f, err := s.openFile("clip/text", proto.Owrite|proto.Otrunc)
			if err != nil {
				t.Fatal(err)
			}
			if replacement != "" {
				if _, err := fs.WriteAt(f, []byte(replacement), 0); err != nil {
					t.Fatal(err)
				}
			}
			if err := f.Close(); err != nil {
				t.Fatal(err)
			}
			got, err := fs.ReadFile(ns, "clip/text")
			if err != nil || string(got) != replacement {
				t.Fatalf("got %q, %v; want %q", got, err, replacement)
			}
		})
	}
}
