//go:build !plan9

package main

import "errors"

func stopLegacyExports() error { return errors.New("legacy cleanup is 9front-only") }
