package p9p

import (
	"context"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/knusbaum/go9p/proto"
	"tractor.dev/wanix/fs"
	"tractor.dev/wanix/fs/memfs"
)

type slowOpenFS struct {
	fs.FS
	entered  chan struct{}
	release  chan struct{}
	canceled chan struct{}
	once     sync.Once
}

func (m *slowOpenFS) StatContext(_ context.Context, name string) (fs.FileInfo, error) {
	return fs.Stat(m.FS, name)
}
func (m *slowOpenFS) OpenContext(ctx context.Context, name string) (fs.File, error) {
	if name == "slow" {
		m.once.Do(func() { close(m.entered) })
		<-ctx.Done()
		close(m.canceled)
		// Simulate a legacy operation that observes cancellation but takes time
		// to finish. The old response must not collide with a reused tag.
		<-m.release
	}
	return m.FS.Open(name)
}

func TestServeIndependentRequestsAndFlushTagReuse(t *testing.T) {
	m := memfs.New()
	for _, name := range []string{"slow", "fast"} {
		if err := fs.WriteFile(m, name, []byte(name), 0644); err != nil {
			t.Fatal(err)
		}
	}
	slow := &slowOpenFS{FS: m, entered: make(chan struct{}), release: make(chan struct{}), canceled: make(chan struct{})}
	a, b := net.Pipe()
	done := make(chan error, 1)
	go func() { done <- Serve(slow, a, a) }()
	t.Cleanup(func() {
		a.Close()
		b.Close()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("Serve did not stop")
		}
	})
	if err := b.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	send := func(call proto.FCall) {
		t.Helper()
		if _, err := b.Write(call.Compose()); err != nil {
			t.Fatal(err)
		}
	}
	receive := func(tag uint16, typ uint8) proto.FCall {
		t.Helper()
		r, err := proto.ParseCall(b)
		if err != nil {
			t.Fatal(err)
		}
		if r.GetTag() != tag || r.Compose()[4] != typ {
			t.Fatalf("response %T tag %d, want type %d tag %d", r, r.GetTag(), typ, tag)
		}
		return r
	}
	send(&proto.TAttach{Header: proto.Header{Type: proto.Tattach, Tag: 1}, Fid: 1})
	receive(1, proto.Rattach)
	for i, name := range []string{"slow", "fast"} {
		tag := uint16(i + 2)
		send(&proto.TWalk{Header: proto.Header{Type: proto.Twalk, Tag: tag}, Fid: 1, Newfid: uint32(i + 2), Nwname: 1, Wname: []string{name}})
		receive(tag, proto.Rwalk)
	}
	send(&proto.TOpen{Header: proto.Header{Type: proto.Topen, Tag: 10}, Fid: 2, Mode: proto.Oread})
	select {
	case <-slow.entered:
	case <-time.After(time.Second):
		t.Fatal("slow open did not start")
	}
	send(&proto.TOpen{Header: proto.Header{Type: proto.Topen, Tag: 11}, Fid: 3, Mode: proto.Oread})
	receive(11, proto.Ropen)
	send(&proto.TFlush{Header: proto.Header{Type: proto.Tflush, Tag: 12}, Oldtag: 10})
	receive(12, proto.Rflush)
	select {
	case <-slow.canceled:
	case <-time.After(time.Second):
		t.Fatal("flush did not cancel context")
	}
	send(&proto.TStat{Header: proto.Header{Type: proto.Tstat, Tag: 10}, Fid: 3})
	receive(10, proto.Rstat)
	close(slow.release)
	// A same-fid barrier guarantees the slow operation has returned before
	// checking the wire. Any stale Ropen would precede this response.
	send(&proto.TClunk{Header: proto.Header{Type: proto.Tclunk, Tag: 13}, Fid: 2})
	receive(13, proto.Rclunk)
	send(&proto.TRead{Header: proto.Header{Type: proto.Tread, Tag: 14}, Fid: 3, Count: 10})
	r := receive(14, proto.Rread).(*proto.RRead)
	if string(r.Data) != "fast" {
		t.Fatalf("read = %q", r.Data)
	}
	b.Close()
	if err := <-done; err != nil && err != io.EOF {
		t.Fatal(err)
	}
	done <- nil
}
