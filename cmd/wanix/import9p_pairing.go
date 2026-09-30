//go:build !js && !wasm

package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

func writeMountPairingFile(name, servedDir, token string) error {
	if name == "" {
		return errors.New("a private pairing-code file is required")
	}
	name, err := filepath.Abs(name)
	if err != nil {
		return err
	}
	// Resolve parents so a symlink cannot place the private file under HTTP root.
	parent, err := filepath.EvalSymlinks(filepath.Dir(name))
	if err != nil {
		return err
	}
	name = filepath.Join(parent, filepath.Base(name))
	servedDir, err = filepath.EvalSymlinks(servedDir)
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(servedDir, name)
	if err != nil {
		return err
	}
	if relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("pairing file must be outside the HTTP-served directory")
	}
	file, err := os.CreateTemp(parent, ".wanix-mount-pairing-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err := file.WriteString(token + "\n"); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), name)
}
