//go:build js && wasm

// Command kibitz-wasm is the browser core: wire codec, PAKE + group crypto,
// session engine, service mux, and game rules all run in WASM. This package
// is the ONLY place syscall/js may be imported — everything below it compiles
// natively too, which is what makes the headless integration tests real.
package main

func main() {
	// M1-8 installs the kibitz_send / kibitzOnEvent JSON bridge here and
	// blocks forever. Until then this is a compile-check stub.
	println("kibitz wasm core loaded")
	select {}
}
