# Build, test and install the schema plugin.
GO ?= go
WASM := plugin.wasm
PLUGINS_DIR ?= ../../data/plugins

.PHONY: build test vet clean install

build: $(WASM)

$(WASM): $(wildcard *.go) go.mod
	GOOS=wasip1 GOARCH=wasm $(GO) build -buildmode=c-shared -trimpath -o $(WASM) .

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...
	GOOS=wasip1 GOARCH=wasm $(GO) vet ./...

clean:
	rm -f $(WASM)

# Copy the manifest and the module into a plugins directory, then use
# "Reload" in Admin → Plugins.
install: build
	mkdir -p $(PLUGINS_DIR)/schema
	cp plugin.json $(WASM) $(PLUGINS_DIR)/schema/
