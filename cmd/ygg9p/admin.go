package main

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"tractor.dev/wanix/internal/ygg9pauth"
)

func publicKey(id string) (ed25519.PublicKey, error) {
	b, err := hex.DecodeString(id)
	if err != nil || len(b) != 32 || hex.EncodeToString(b) != id || ygg9pauth.Address(b) == nil {
		return nil, errors.New("expected a canonical 32-byte hex Yggdrasil public key")
	}
	return ed25519.PublicKey(b), nil
}
func allocateImage(config, id string) error {
	if _, err := publicKey(id); err != nil {
		return err
	}
	if !filepath.IsAbs(config) {
		return errors.New("absolute config path required")
	}
	dir := filepath.Join(filepath.Dir(config), "images")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, id+".img"), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	// The image's logical length is its hard block limit in hjfs. No user can
	// extend the image through its exported filesystem.
	return f.Truncate(1 << 30)
}
func enrollKey(config, id string, transports ...string) error {
	for _, transport := range transports {
		if _, err := publicKey(transport); err != nil {
			return err
		}
	}
	if _, err := publicKey(id); err != nil {
		return err
	}
	if !filepath.IsAbs(config) {
		return errors.New("absolute config path required")
	}
	unlock, err := lockConfig(config)
	if err != nil {
		return err
	}
	defer unlock()
	b, err := os.ReadFile(config)
	if err != nil {
		return err
	}
	var cfg configuration
	if err := json.Unmarshal(b, &cfg); err != nil {
		return err
	}
	for _, e := range cfg.Exports {
		if e.PublicKey == id {
			return errors.New("key already enrolled")
		}
	}
	base := filepath.Dir(config)
	e := exportConfig{TransportKeys: transports, PublicKey: id, Root: filepath.Join(base, "mnt", id, "data"), Access: "read-write", Image: filepath.Join(base, "images", id+".img"), QuotaBytes: 1 << 30}
	st, err := os.Stat(e.Image)
	if err != nil || st.Size() != e.QuotaBytes {
		return errors.New("provision the 1 GiB image first")
	}
	cfg.Exports = append(cfg.Exports, e)
	return publishConfig(config, cfg)
}

// Serialize the entire read-modify-publish transaction, including concurrent
// GUI and CLI administrators. A crash leaves a lock for manual recovery.
func lockConfig(config string) (func(), error) {
	if !filepath.IsAbs(config) {
		return nil, errors.New("absolute config path required")
	}
	path := config + ".lock"
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, errors.New("configuration busy; check administrator lock")
	}
	f.Close()
	return func() { os.Remove(path) }, nil
}
func publishConfig(config string, cfg configuration) error {
	b, err := json.MarshalIndent(cfg, "", "  ")

	if err != nil {
		return err
	}
	tmp := config + ".new"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if _, err := load(tmp); err != nil {
		os.Remove(tmp)
		return err
	}
	// Plan 9 cannot rename over an existing file. Preserve the old allowlist
	// before publishing the new one; the brief gap fails closed for new mounts.
	backup := config + ".backup-" + time.Now().UTC().Format("20060102T150405.000000000")
	if err := os.Rename(config, backup); err != nil {
		return err
	}
	if err := os.Rename(tmp, config); err != nil {
		os.Rename(backup, config)
		return err
	}
	return nil
}

// Revocation removes authorization for future connections only. Keep the
// backing image and data, and never silently disconnect an active session.
func revokeKey(config, id string) error {
	if _, err := publicKey(id); err != nil {
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
	found := false
	for i, e := range cfg.Exports {
		if e.PublicKey == id {
			cfg.Exports = append(cfg.Exports[:i], cfg.Exports[i+1:]...)
			found = true
			break
		}
	}
	if !found {
		return errors.New("key is not enrolled")
	}
	return publishConfig(config, cfg)
}
func listKeys(config string, out io.Writer) error {
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
	for _, e := range cfg.Exports {
		transport := strings.Join(e.TransportKeys, ",")
		if transport == "" {
			transport = "direct"
		}
		if _, err := fmt.Fprintf(out, "%s\t%s\t%s\t%d\n", e.PublicKey, transport, e.Access, e.QuotaBytes); err != nil {
			return err
		}
	}
	return nil
}
