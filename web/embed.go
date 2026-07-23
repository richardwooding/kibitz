// Package web embeds the built browser client. `make wasm` populates dist/
// (copied src assets + kibitz.wasm + wasm_exec.js); the relay binary serves
// it, so self-hosting is a single executable.
package web

import "embed"

//go:embed all:dist
var Dist embed.FS
