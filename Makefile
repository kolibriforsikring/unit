BIN_DIR      := bin
UNIT_CLI     := $(BIN_DIR)/unit
INSPECTOR    := $(BIN_DIR)/unit-inspector

.PHONY: all build clean vet lint vuln check

all: build

build:
	@mkdir -p $(BIN_DIR)
	@echo "🔨 Building unit..."
	go build -o $(UNIT_CLI) ./cmd/unit
	@echo "📦 Building unit-inspector (static Linux/amd64)..."
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o $(INSPECTOR) ./cmd/unit-inspector
	@echo "✅ Build complete. Binaries are in $(BIN_DIR)/"

vet:
	go vet ./...

lint:
	staticcheck ./...

vuln:
	govulncheck ./...

# Run all quality checks
check: vet lint vuln

clean:
	@echo "🧹 Cleaning up..."
	rm -rf $(BIN_DIR)
