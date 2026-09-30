//go:build !js && !wasm

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestImport9PPrivatePairingFile(t *testing.T) {
	root := t.TempDir()
	served := filepath.Join(root, "public")
	if err := os.Mkdir(served, 0755); err != nil {
		t.Fatal(err)
	}
	private := filepath.Join(root, "pairing")
	if err := writeMountPairingFile(private, served, "first"); err != nil {
		t.Fatal(err)
	}
	if err := writeMountPairingFile(private, served, "second"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(private)
	if err != nil || string(data) != "second\n" {
		t.Fatalf("%q %v", data, err)
	}
	info, err := os.Stat(private)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("pairing code permissions %v", info.Mode())
	}
	if err := writeMountPairingFile(filepath.Join(served, "exposed"), served, "secret"); err == nil {
		t.Fatal("pairing code exposed by HTTP")
	}
	alias := filepath.Join(root, "public-alias")
	if err := os.Symlink(served, alias); err != nil {
		t.Fatal(err)
	}
	if err := writeMountPairingFile(filepath.Join(alias, "exposed"), served, "secret"); err == nil {
		t.Fatal("symlink exposed pairing code")
	}
}
