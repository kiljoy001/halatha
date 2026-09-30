# Running the v86 module tests

This module is `package main` under `//go:build js && wasm`, so its tests
need the js/wasm toolchain and a Node runtime. A full environment overflows
the wasm command-line limit, so run with a trimmed env:

```sh
cd v86
env -i \
  PATH="/usr/bin:/bin:$(dirname $(which node)):$(go env GOROOT)/lib/wasm" \
  HOME="$HOME" \
  GOOS=js GOARCH=wasm \
  GOCACHE=$(go env GOCACHE) \
  GOMODCACHE=$(go env GOMODCACHE) \
  $(which go) test -v .
```

Without the trimmed env you get:
`Error: total length of command line and environment variables exceeds limit`
