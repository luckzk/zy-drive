GO ?= go
GOFLAGS ?= -trimpath

.PHONY: all tidy vet test build build-frontend build-desktop build-agent build-cli clean run-desktop run-agent app check-icon

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

# macOS .app bundle with the Dock icon (make build only produces a
# bare binary, which gets a generic Dock icon — the icon only
# exists inside a .app). Usage on the Mac:
#   1. node scripts/visual-check/export-logo-png.mjs   # once: regenerate resources/icon.png (PNG is binary, cannot be pushed)
#   2. make app                                          # produces build/darwin-arm64/ZyDrive.app
#   3. Drag build/darwin-arm64/ZyDrive.app into Applications, or double-click it.
# -skip-dmg: the .dmg needs macOS packaging tools; the .app alone
# is enough to run. APP_PLATFORM can be overridden, e.g.
# `make app APP_PLATFORM=darwin/universal`.
APP_PLATFORM ?= darwin/arm64

check-icon:
	@test -f resources/icon.png || { echo "resources/icon.png is missing — the .app would get a generic icon."; echo "Regenerate it first (PNG is binary and cannot be pushed to the repo):"; echo "  node scripts/visual-check/export-logo-png.mjs"; exit 1; }

app: check-icon
	@command -v mygo >/dev/null 2>&1 || { echo "mygo CLI not found in PATH. Install it with:"; echo "  go install github.com/egoist/mygo/cmd/mygo@v0.10.0"; exit 1; }
	mygo build -platform $(APP_PLATFORM) -skip-dmg

clean:
	rm -rf build
