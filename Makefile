GO ?= go
GOFLAGS ?= -trimpath

.PHONY: all tidy vet test build build-frontend build-desktop build-agent build-cli clean run-desktop run-agent

all: tidy vet test build

tidy:
	$(GO) mod tidy

vet:
	$(GO) vet ./...

test:
	$(GO) test ./...

build: build-frontend build-desktop build-agent build-cli

build-frontend:
	cd frontend && npm install && npm run build

build-desktop: build-frontend
	$(GO) build $(GOFLAGS) -o build/zy-drive-desktop ./cmd/desktop

build-agent:
	$(GO) build $(GOFLAGS) -o build/zy-drive-agent ./cmd/agent

build-cli:
	$(GO) build $(GOFLAGS) -o build/zy-drive ./cmd/cli

run-desktop: build-desktop
	./build/zy-drive-desktop --config-dir $(HOME)/.config/zy-drive

run-agent: build-agent
	./build/zy-drive-agent --addr 127.0.0.1:8080 --config-dir $(HOME)/.config/zy-drive-agent

clean:
	rm -rf build
