//go:build !(js && wasm)

// Native stub so `go build ./...` and `go vet ./...` succeed on the host —
// the real entrypoint is main.go, built only under GOOS=js GOARCH=wasm.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "kibitz-wasm is a WebAssembly binary; build it with: GOOS=js GOARCH=wasm go build ./cmd/kibitz-wasm")
	os.Exit(1)
}
