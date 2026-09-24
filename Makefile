GO      ?= go
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS ?= -s -w -X main.version=$(VERSION)

BINARY = airplay2-receiver
MAIN   = ./cmd/airplay2-receiver

# Install dir: default to ~/.local/bin; allow PREFIX or BINDIR overrides.
ifdef PREFIX
BINDIR ?= $(PREFIX)/bin
else
BINDIR ?= $(HOME)/.local/bin
endif

.PHONY: all help build test vet install uninstall cross clean

all: build

help:
	@echo "gap2 make targets:"
	@echo "  build      build $(BINARY) (default)"
	@echo "  test       run go test"
	@echo "  vet        run go vet"
	@echo "  install    install to \$$(BINDIR) [$(BINDIR)]"
	@echo "  uninstall  remove installed binary"
	@echo "  cross      cross-build linux/amd64 linux/arm64 darwin/arm64 into dist/"
	@echo "  clean      remove build output"
	@echo ""
	@echo "variables: PREFIX (forces \$$PREFIX/bin), BINDIR, DESTDIR, GO [$(GO)]"
	@echo "default install dir: ~/.local/bin"

build:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o $(BINARY) $(MAIN)

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

install: build
	install -d "$(DESTDIR)$(BINDIR)"
	install -m 0755 $(BINARY) "$(DESTDIR)$(BINDIR)/$(BINARY)"

uninstall:
	rm -f "$(DESTDIR)$(BINDIR)/$(BINARY)"

cross:
	@mkdir -p dist
	@for target in linux/amd64 linux/arm64 darwin/arm64; do \
		goos=$${target%/*}; goarch=$${target#*/}; \
		echo "building $$goos/$$goarch"; \
		CGO_ENABLED=0 GOOS=$$goos GOARCH=$$goarch $(GO) build -trimpath \
			-ldflags '$(LDFLAGS)' -o "dist/$(BINARY)-$$goos-$$goarch" $(MAIN) || exit 1; \
	done

clean:
	rm -f $(BINARY)
	rm -rf dist
