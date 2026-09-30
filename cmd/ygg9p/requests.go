package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"
)

const maxPendingRequests = 64

type keyRequest struct {
	ID               string `json:"id"`
	Action           string `json:"action"`
	ClientKey        string `json:"clientKey"`
	TransportKey     string `json:"transportKey"`
	ObservedPeer     string `json:"observedPeer,omitempty"`
	Created          int64  `json:"created"`
	IdentityVerified bool   `json:"identityVerified"`
}
type requestSubmission struct {
	Action       string `json:"action"`
	ClientKey    string `json:"clientKey"`
	TransportKey string `json:"transportKey"`
}

func requestDir(config string) string { return filepath.Join(filepath.Dir(config), "requests") }
func pendingRequests(config string) ([]keyRequest, error) {
	entries, err := os.ReadDir(requestDir(config))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(entries) > maxPendingRequests {
		return nil, errors.New("pending request directory exceeds limit")
	}
	var requests []keyRequest
	for _, entry := range entries {
		if entry.IsDir() {
			return nil, errors.New("invalid request record")
		}
		f, err := os.Open(filepath.Join(requestDir(config), entry.Name()))
		if err != nil {
			return nil, err
		}
		var r keyRequest
		d := json.NewDecoder(io.LimitReader(f, 2049))
		d.DisallowUnknownFields()
		err = d.Decode(&r)
		if err == nil && d.Decode(new(any)) != io.EOF {
			err = errors.New("invalid request data")
		}
		f.Close()
		if err != nil {
			return nil, err
		}
		if !validRequestID(r.ID) || entry.Name() != r.ID+".json" || validateSubmission(requestSubmission{r.Action, r.ClientKey, r.TransportKey}) != nil {
			return nil, errors.New("invalid request record")
		}
		requests = append(requests, r)
	}
	sort.Slice(requests, func(i, j int) bool { return requests[i].Created < requests[j].Created })
	return requests, nil
}
func validateSubmission(s requestSubmission) error {
	if s.Action != "register" && s.Action != "change-transport" {
		return errors.New("only registration or transport-change requests are accepted")
	}
	if _, err := publicKey(s.ClientKey); err != nil {
		return err
	}
	if _, err := publicKey(s.TransportKey); err != nil {
		return err
	}
	return nil
}
func submitRequest(config string, s requestSubmission, peer ...string) (keyRequest, error) {
	var r keyRequest
	if err := validateSubmission(s); err != nil {
		return r, err
	}
	if !filepath.IsAbs(config) {
		return r, errors.New("absolute config path required")
	}
	unlock, err := lockConfig(config + ".requests")
	if err != nil {
		return r, err
	}
	defer unlock()
	records, err := pendingRequests(config)
	if err != nil {
		return r, err
	}
	if len(records) >= maxPendingRequests {
		return r, errors.New("request inbox full; contact administrator")
	}
	for _, old := range records {
		if old.ClientKey == s.ClientKey {
			return r, errors.New("this client already has a pending request")
		}
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return r, err
	}
	r = keyRequest{ID: hex.EncodeToString(nonce[:]), Action: s.Action, ClientKey: s.ClientKey, TransportKey: s.TransportKey, Created: time.Now().UnixMilli(), IdentityVerified: false}
	if len(peer) != 0 {
		r.ObservedPeer = peer[0]
	}
	if err := os.MkdirAll(requestDir(config), 0700); err != nil {
		return r, err
	}
	b, err := json.Marshal(r)
	if err != nil {
		return r, err
	}
	f, err := os.OpenFile(filepath.Join(requestDir(config), r.ID+".json"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return r, err
	}
	n, err := f.Write(b)
	if err == nil && n != len(b) {
		err = io.ErrShortWrite
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		os.Remove(filepath.Join(requestDir(config), r.ID+".json"))
	}
	return r, err
}
func validRequestID(id string) bool {
	b, err := hex.DecodeString(id)
	return err == nil && len(b) == 16 && hex.EncodeToString(b) == id
}
func resolveRequest(config, id string) error {
	if !validRequestID(id) {
		return errors.New("invalid request ID")
	}
	unlock, err := lockConfig(config + ".requests")
	if err != nil {
		return err
	}
	defer unlock()
	return os.Remove(filepath.Join(requestDir(config), id+".json"))
}
func listRequests(config string, out io.Writer) error {
	unlock, err := lockConfig(config + ".requests")
	if err != nil {
		return err
	}
	defer unlock()
	records, err := pendingRequests(config)
	if err != nil {
		return err
	}
	for _, r := range records {
		if _, err := fmt.Fprintf(out, "%s\t%s\t%s\t%s\t%s\n", r.ClientKey, r.TransportKey, r.Action, r.ID, r.ObservedPeer); err != nil {
			return err
		}
	}
	return nil
}
func updateTransport(config, id, transport string) error {
	if _, err := publicKey(id); err != nil {
		return err
	}
	if _, err := publicKey(transport); err != nil {
		return err
	}
	unlock, err := lockConfig(config)
	if err != nil {
		return err
	}
	defer unlock()
	if _, err := load(config); err != nil {
		return err
	}
	b, err := os.ReadFile(config)
	if err != nil {
		return err
	}
	var cfg configuration
	if err := json.Unmarshal(b, &cfg); err != nil {
		return err
	}
	for i := range cfg.Exports {
		if cfg.Exports[i].PublicKey == id {
			cfg.Exports[i].TransportKeys = []string{transport}
			return publishConfig(config, cfg)
		}
	}
	return errors.New("client is not enrolled")
}
