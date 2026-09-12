//go:build windows

package main

// rsrc is pinned by version here instead of in go.mod: it is a generator-only
// package main that `go mod tidy` would drop, since no Go file imports it.
//go:generate go run github.com/akavel/rsrc@v0.10.2 -arch amd64 -manifest resolution-tray.manifest -o rsrc.syso
