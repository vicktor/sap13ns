# Build, test and install the YouTube gallery plugin.
GO ?= go
WASM := plugin.wasm
PLUGINS_DIR ?= ../../data/plugins

.PHONY: build test vet clean install

build: $(WASM)

$(WASM): $(wildcard *.go) go.mod
	GOOS=wasip1 GOARCH=wasm $(GO) build -buildmode=c-shared -trimpath -ldflags="-s -w" -o $(WASM) .

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...
	GOOS=wasip1 GOARCH=wasm $(GO) vet ./...

clean:
	rm -f $(WASM)

# Copy the manifest, the module and the assets into a plugins directory,
# then use "Reload" in Admin → Plugins.
install: build
	mkdir -p $(PLUGINS_DIR)/youtube/assets
	cp plugin.json $(WASM) $(PLUGINS_DIR)/youtube/
	cp assets/youtube.css assets/youtube.js $(PLUGINS_DIR)/youtube/assets/
