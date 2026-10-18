# Build, test and package the my-plugin plugin.
#
#   make build   compile plugin.wasm
#   make test    run the tests natively (no WebAssembly needed)
#   make check   scan the plugin as the server will when you upload it
#   make pack    build, check and write my-plugin-<version>.zip + .sha256
#
# check and pack use the sap13ns binary; set SAP13NS=/path/to/sap13ns if it
# is not on your PATH.
GO ?= go
SAP13NS ?= sap13ns
WASM := plugin.wasm

.PHONY: build test vet check pack clean

build: $(WASM)

$(WASM): $(wildcard *.go) go.mod
	GOOS=wasip1 GOARCH=wasm $(GO) build -buildmode=c-shared -trimpath -o $(WASM) .

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...
	GOOS=wasip1 GOARCH=wasm $(GO) vet ./...

check: build
	$(SAP13NS) plugin check .

pack: test build
	$(SAP13NS) plugin pack -no-build .

clean:
	rm -f $(WASM) *.zip *.zip.sha256
