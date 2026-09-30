package p9p

import (
	"context"
	"io"
	"sync"

	"github.com/knusbaum/go9p/proto"
	"tractor.dev/wanix/fs"
)

type request struct {
	cancel context.CancelFunc
	done   chan struct{}
	fids   []uint32
}

type reply struct {
	request *request
	call    proto.FCall
}

// Serve dispatches independent fids concurrently. Requests touching the same
// fid remain ordered. The coordinator alone writes replies and owns tags, so
// no response for a flushed request can follow Rflush, even after tag reuse.
func Serve(fsys fs.FS, r io.Reader, w io.Writer) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type incoming struct {
		call proto.FCall
		err  error
	}
	calls := make(chan incoming)
	replies := make(chan reply)
	go func() {
		for {
			call, err := proto.ParseCall(r)
			select {
			case calls <- incoming{call, err}:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	c := &conn{fids: make(map[uint32]*fidState), msize: proto.MaxMsgLen}
	pending := make(map[uint16]*request)
	tails := make(map[uint32]chan struct{})
	var workers sync.WaitGroup
	defer func() {
		cancel()
		// A blocked legacy fs.File operation cannot be forcibly interrupted. Close
		// handles after it returns; never race Close against a running operation.
		go func() {
			workers.Wait()
			for _, f := range c.fids {
				if f.file != nil {
					f.file.Close()
				}
			}
		}()
	}()
	send := func(resp proto.FCall) error {
		data := resp.Compose()
		n, err := w.Write(data)
		if err == nil && n != len(data) {
			err = io.ErrShortWrite
		}
		return err
	}
	for {
		select {
		case in := <-calls:
			if in.err != nil {
				if in.err == io.EOF {
					return nil
				}
				return in.err
			}
			call := in.call
			tag := call.GetTag()
			if flush, ok := call.(*proto.TFlush); ok {
				if old := pending[flush.Oldtag]; old != nil {
					old.cancel()
					delete(pending, flush.Oldtag)
				}
				if err := send(&proto.RFlush{Header: proto.Header{Type: proto.Rflush, Tag: tag}}); err != nil {
					return err
				}
				continue
			}
			if _, exists := pending[tag]; exists {
				// A duplicate in-flight tag cannot be answered unambiguously.
				return fs.ErrInvalid
			}
			if version, ok := call.(*proto.TRVersion); ok {
				// Version negotiation is a session boundary, and cannot race operations
				// using the old fid table or msize. Refuse renegotiation while busy.
				busy := false
				for _, done := range tails {
					select {
					case <-done:
					default:
						busy = true
					}
				}
				if busy {
					if err := send(rerr(tag, "version negotiation while requests are active")); err != nil {
						return err
					}
					continue
				}
				for _, f := range c.fids {
					if f.file != nil {
						f.file.Close()
					}
				}
				c.fids = make(map[uint32]*fidState)
				clear(tails)
				for _, req := range pending {
					req.cancel()
				}
				clear(pending)
				if err := send((&srv{Fsys: fsys}).version(c, version)); err != nil {
					return err
				}
				continue
			}
			reqctx, reqcancel := context.WithCancel(ctx)
			req := &request{cancel: reqcancel, done: make(chan struct{}), fids: requestFids(call)}
			pending[tag] = req
			var before []chan struct{}
			for _, id := range req.fids {
				if tail := tails[id]; tail != nil {
					before = append(before, tail)
				}
				tails[id] = req.done
			}
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer reqcancel()
				// Even a canceled waiter preserves ordering for later requests.
				for _, done := range before {
					select {
					case <-done:
					case <-ctx.Done():
						close(req.done)
						return
					}
				}
				var resp proto.FCall
				if reqctx.Err() == nil {
					resp = (&srv{Fsys: fsys, ctx: reqctx}).handle(c, call)
				}
				close(req.done)
				select {
				case replies <- reply{req, resp}:
				case <-ctx.Done():
				}
			}()
		case out := <-replies:
			for _, id := range out.request.fids {
				if tails[id] == out.request.done {
					delete(tails, id)
				}
			}
			if out.call == nil {
				continue
			}
			tag := out.call.GetTag()
			if pending[tag] != out.request {
				continue
			}
			delete(pending, tag)
			if err := send(out.call); err != nil {
				return err
			}
		}
	}
}

func requestFids(call proto.FCall) []uint32 {
	switch t := call.(type) {
	case *proto.TAttach:
		return []uint32{t.Fid}
	case *proto.TWalk:
		if t.Fid == t.Newfid {
			return []uint32{t.Fid}
		}
		return []uint32{t.Fid, t.Newfid}
	case *proto.TOpen:
		return []uint32{t.Fid}
	case *proto.TCreate:
		return []uint32{t.Fid}
	case *proto.TRead:
		return []uint32{t.Fid}
	case *proto.TWrite:
		return []uint32{t.Fid}
	case *proto.TClunk:
		return []uint32{t.Fid}
	case *proto.TRemove:
		return []uint32{t.Fid}
	case *proto.TStat:
		return []uint32{t.Fid}
	case *proto.TWstat:
		return []uint32{t.Fid}
	}
	return nil
}
