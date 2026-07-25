# kibitz build targets. `make wasm` must run before the relay binary is built
# for the embedded web client to include the browser core.

GOROOT := $(shell go env GOROOT)

.PHONY: web wasm serve test lint clean

## web: copy static assets + wasm_exec.js into web/dist
web:
	mkdir -p web/dist
	cp web/src/* web/dist/
	cp "$(GOROOT)/lib/wasm/wasm_exec.js" web/dist/

## wasm: build the browser core into web/dist (implies web), then precompress
## every servable asset (wasm + JS/CSS/HTML) to .br (brotli) and .gz (gzip) so
## the relay serves the smallest encoding each client accepts. Pure-Go
## compressor — no external gzip/brotli binary needed in CI.
wasm: web
	GOOS=js GOARCH=wasm go build -trimpath -ldflags="-s -w" -o web/dist/kibitz.wasm ./cmd/kibitz-wasm
	go run ./cmd/compress-assets web/dist

## serve: dev loop — build everything and run the relay on :8080
serve: wasm
	go run ./cmd/kibitz

## test: full test suite with the race detector
test:
	go test -race ./...

## lint: vet + golangci-lint (matches CI)
lint:
	go vet ./...
	golangci-lint run

clean:
	rm -f kibitz
	find web/dist -type f ! -name .gitkeep -delete
