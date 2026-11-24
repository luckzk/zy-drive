GO ?= $(HOME)/tools/go/bin/go
GOFLAGS ?= -trimpath

.PHONY: all tidy vet test build build-desktop build-agent build-cli clean run-desktop run-agent

all: tidy vet test build

tidy:
	$(GO) mod tidy

vet:
	$(GO) vet ./...

test:
	$(GO) test ./...

build: build-desktop build-agent build-cli

build-desktop:
	$(GO) build $(GOFLAGS) -o build/mcm-desktop ./cmd/desktop

build-agent:
	$(GO) build $(GOFLAGS) -o build/mcm-agent ./cmd/agent

build-cli:
	$(GO) build $(GOFLAGS) -o build/mcm ./cmd/cli

run-desktop: build-desktop
	./build/mcm-desktop --config-dir $(HOME)/.config/zy-drive

run-agent: build-agent
	./build/mcm-agent --addr 127.0.0.1:8080 --config-dir $(HOME)/.config/zy-drive-agent

clean:
	rm -rf build
