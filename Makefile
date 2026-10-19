# Development shortcuts. Releases are built by .github/workflows/release.yml.
GO      ?= go
ENV     ?= data/dev.env
# The development network of make dev-network (docs/MULTISITE.md).
NET_DB   ?= sap13ns_net_dev
NET_PORT ?= 8139
NET_ENV  ?= data/network.env
NET_DATA ?= data/network
# A tagged commit builds as that version; anything else is "dev", which the
# update checker never reports as out of date.
VERSION ?= $(shell git describe --tags --exact-match 2>/dev/null || echo dev)
LDFLAGS := -s -w -X sap13ns/internal/web.Version=$(VERSION)
PLUGINS := $(dir $(wildcard plugins/*/Makefile))
# The official plugins (every plugin but the skeleton) and the bundle of
# their packages, which the binary embeds (internal/plugin/bundled).
OFFICIAL := $(filter-out skeleton,$(patsubst plugins/%/plugin.json,%,$(wildcard plugins/*/plugin.json)))
BUNDLE   := internal/plugin/bundled/packs

.PHONY: all assets build test vet i18n check plugins plugins-test dev seed dev-network clean

all: assets build

# Tailwind CSS, Basecoat JS and Lucide icons (outputs are committed).
assets: node_modules
	npm run build

node_modules: package-lock.json
	npm ci
	@touch $@

# The binary, with the official plugins inside.
build: plugins
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o bin/sap13ns ./cmd/sap13ns

vet:
	$(GO) vet ./...

# Database tests need SAP13NS_TEST_DATABASE_URL or postgres:///sap13ns_test?host=/tmp.
test:
	$(GO) test ./...
	npm test

# Every UI string must have a Spanish translation.
i18n:
	$(GO) run ./scripts/i18ncheck -lang es -strict

check: vet test i18n plugins-test

# Each plugin is its own Go module with its own Makefile. This builds them
# all, then packs and scans the official ones into $(BUNDLE) (generated,
# not committed); a package that did not change is kept as it is.
plugins:
	@for p in $(PLUGINS); do $(MAKE) -C $$p build || exit 1; done
	$(GO) run ./scripts/pluginbundle -o $(BUNDLE) $(addprefix plugins/,$(OFFICIAL))

plugins-test:
	cd pluginsdk && $(GO) test ./...
	@for p in $(PLUGINS); do $(MAKE) -C $$p test || exit 1; done

dev: build
	./bin/sap13ns serve -env $(ENV)

seed:
	$(GO) run ./scripts/devseed -env $(ENV)

# A network of sites with sample content: the portal on localhost:$(NET_PORT),
# the sites on <slug>.localhost:$(NET_PORT). RESET=1 starts it over. Serve it
# with make dev ENV=$(NET_ENV).
dev-network:
	$(GO) run ./scripts/devnetwork -db $(NET_DB) -port $(NET_PORT) -env $(NET_ENV) -data $(NET_DATA)$(if $(RESET), -reset)

clean:
	rm -rf bin dist
	rm -f $(BUNDLE)/*.zip
	@for p in $(PLUGINS); do $(MAKE) -C $$p clean; done
