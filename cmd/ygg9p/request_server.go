package main

import (
	"bytes"
	"encoding/json"
	"io"
	"time"

	"9fans.net/go/plan9"
	"tractor.dev/wanix/internal/ygg9pauth"
)

// A deliberately small 9P service: request accepts a proposal and status
// returns only this connection's receipt. No registry, mounts or admin RPC.
func serveRequests(in io.Reader, out io.Writer, config string, observedPeer ...string) error {
	type fidState struct {
		path   string
		opened bool
		mode   uint8
	}
	fids := map[uint32]fidState{}
	attached, submitted := false, false
	status := []byte("No request submitted. Administrator approval is required.\n")
	msize := uint32(ygg9pauth.MaxSize)
	version := uint32(0)
	qid := func(path string) plan9.Qid {
		q := plan9.Qid{Path: 1}
		if path == "" {
			q.Type = plan9.QTDIR
		}
		if path == "request" {
			q.Path = 2
		}
		if path == "status" {
			q.Path = 3
			q.Vers = version
		}
		return q
	}
	stat := func(path string) ([]byte, error) {
		name := path
		mode := uint32(0444)
		length := uint64(0)
		if path == "" {
			name = "/"
			mode = plan9.DMDIR | 0555
		}
		if path == "request" {
			mode = 0222
		}
		if path == "status" {
			length = uint64(len(status))
		}
		return (&plan9.Dir{Name: name, Uid: "none", Gid: "none", Muid: "none", Qid: qid(path), Mode: plan9.Perm(mode), Length: length, Mtime: uint32(time.Now().Unix()), Atime: uint32(time.Now().Unix())}).Bytes()
	}
	for {
		f, err := ygg9pauth.Read(in)
		if err != nil {
			return err
		}
		r := &plan9.Fcall{Type: f.Type + 1, Tag: f.Tag}
		fail := func(reason string) { r = &plan9.Fcall{Type: plan9.Rerror, Tag: f.Tag, Ename: reason} }
		s, exists := fids[f.Fid]
		switch f.Type {
		case plan9.Tversion:
			if attached || f.Version != "9P2000" || f.Msize < 256 {
				fail("9P2000 required")
			} else {
				msize = min(f.Msize, uint32(ygg9pauth.MaxSize))
				r.Msize = msize
				r.Version = "9P2000"
			}
		case plan9.Tauth:
			fail("authentication is not required for submitting an unverified proposal")
		case plan9.Tattach:
			if attached || f.Aname != "" || f.Afid != plan9.NOFID || f.Fid == plan9.NOFID {
				fail("one request namespace only")
			} else {
				attached = true
				fids[f.Fid] = fidState{}
				r.Qid = qid("")
			}
		case plan9.Twalk:
			if !exists || s.opened || len(fids) >= 32 || f.Newfid == plan9.NOFID {
				fail("invalid fid")
				break
			}
			if _, ok := fids[f.Newfid]; ok && f.Newfid != f.Fid {
				fail("fid already exists")
				break
			}
			path := s.path
			valid := true
			for _, name := range f.Wname {
				if name == "." {
				} else if name == ".." {
					path = ""
				} else if path == "" && (name == "request" || name == "status") {
					path = name
				} else {
					valid = false
					break
				}
				r.Wqid = append(r.Wqid, qid(path))
			}
			if !valid {
				fail("not found")
			} else {
				fids[f.Newfid] = fidState{path: path}
			}
		case plan9.Topen:
			if !exists || s.opened {
				fail("invalid fid")
				break
			}
			if (s.path == "request" && (f.Mode != plan9.OWRITE && f.Mode != (plan9.OWRITE|plan9.OTRUNC))) || (s.path != "request" && f.Mode != plan9.OREAD) {
				fail("operation not allowed")
				break
			}
			s.opened = true
			s.mode = f.Mode
			fids[f.Fid] = s
			r.Qid = qid(s.path)
			r.Iounit = msize - 24
		case plan9.Tread:
			if !exists || !s.opened || s.path == "request" {
				fail("read not allowed")
				break
			}
			count := uint64(min(f.Count, msize-11))
			if s.path == "" {
				a, _ := stat("request")
				b, _ := stat("status")
				entries := [][]byte{a, b}
				offset := uint64(0)
				valid := f.Offset == uint64(len(a)+len(b))
				for _, entry := range entries {
					if f.Offset == offset {
						valid = true
					}
					if valid && offset >= f.Offset {
						if uint64(len(r.Data)+len(entry)) > count {
							break
						}
						r.Data = append(r.Data, entry...)
					}
					offset += uint64(len(entry))
				}
				if !valid {
					fail("directory offset must be an entry boundary")
				} else if len(r.Data) == 0 && f.Offset < uint64(len(a)+len(b)) {
					fail("read buffer too small for directory entry")
				}
			} else if f.Offset < uint64(len(status)) {
				r.Data = status[f.Offset:min(uint64(len(status)), f.Offset+count)]
			}
		case plan9.Twrite:
			if !exists || !s.opened || s.path != "request" || submitted || f.Offset != 0 || len(f.Data) == 0 || len(f.Data) > 1024 {
				fail("one bounded request only")
				break
			}
			var proposal requestSubmission
			decoder := json.NewDecoder(bytes.NewReader(f.Data))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&proposal); err != nil || decoder.Decode(new(any)) != io.EOF {
				fail("invalid request")
				break
			}
			receipt, err := submitRequest(config, proposal, observedPeer...)
			if err != nil {
				fail(err.Error())
				break
			}
			status, _ = json.Marshal(receipt)
			status = append(status, '\n')
			version++
			submitted = true
			r.Count = uint32(len(f.Data))
		case plan9.Tstat:
			if !exists {
				fail("invalid fid")
			} else {
				r.Stat, err = stat(s.path)
				if err != nil {
					return err
				}
			}
		case plan9.Tclunk:
			if !exists {
				fail("invalid fid")
			} else {
				delete(fids, f.Fid)
			}
		case plan9.Tflush:
		default:
			fail("operation not allowed")
		}
		if err := plan9.WriteFcall(out, r); err != nil {
			return err
		}
	}
}
