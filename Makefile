# NOLO River Camera System - Cross-Platform Build System
# Builds all Go binaries for Mac (darwin ARM64) and Linux platforms
# 
# NOTE: Cross-compilation requires OpenCV libraries for target platform.
# For development, use 'make dev' to build for current platform only.

# Variables
BIN_DIR := bin
GOOS_TARGETS := darwin linux
GOARCH_TARGETS := amd64 arm64

# Build info
BUILD_TIME := $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
GIT_COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
VERSION := $(shell git describe --tags --dirty --always 2>/dev/null || echo "dev")

# Linker flags for version info
LDFLAGS := -ldflags "-X main.Version=$(VERSION) -X main.GitCommit=$(GIT_COMMIT) -X main.BuildTime=$(BUILD_TIME)"

# Colors for output
CYAN := \033[36m
GREEN := \033[32m
YELLOW := \033[33m
RESET := \033[0m

.PHONY: all clean help darwin linux binaries
.DEFAULT_GOAL := all

# Main targets
all: clean binaries
	@echo "$(GREEN)✅ All binaries built successfully!$(RESET)"
	@echo "$(CYAN)📁 Binaries location: ./$(BIN_DIR)/$(RESET)"
	@ls -la $(BIN_DIR)/

binaries: darwin linux

darwin: darwin-arm64
linux: linux-amd64 linux-arm64

# Create bin directory
$(BIN_DIR):
	@echo "$(CYAN)📁 Creating $(BIN_DIR) directory...$(RESET)"
	@mkdir -p $(BIN_DIR)

# Individual platform targets
darwin-arm64: $(BIN_DIR)
	@echo "$(YELLOW)🍎 Building for macOS (Apple Silicon)...$(RESET)"
	@$(MAKE) build-platform GOOS=darwin GOARCH=arm64 SUFFIX=-darwin-arm64

linux-amd64: $(BIN_DIR)
	@echo "$(YELLOW)🐧 Building for Linux (x86_64)...$(RESET)"
	@$(MAKE) build-platform GOOS=linux GOARCH=amd64 SUFFIX=-linux-amd64

linux-arm64: $(BIN_DIR)
	@echo "$(YELLOW)🐧 Building for Linux (ARM64)...$(RESET)"
	@$(MAKE) build-platform GOOS=linux GOARCH=arm64 SUFFIX=-linux-arm64

# Build all binaries for a specific platform
build-platform:
	@echo "$(CYAN)  → Building NOLO$(SUFFIX)...$(RESET)"
	@GOOS=$(GOOS) GOARCH=$(GOARCH) go build $(LDFLAGS) -o $(BIN_DIR)/NOLO$(SUFFIX) NOLO.go
	
	@echo "$(CYAN)  → Building ai_commentary$(SUFFIX)...$(RESET)"
	@cd ai_commentary && GOOS=$(GOOS) GOARCH=$(GOARCH) go build $(LDFLAGS) -o ../$(BIN_DIR)/ai_commentary$(SUFFIX) main.go
	
	@echo "$(CYAN)  → Building broadcast$(SUFFIX)...$(RESET)"
	@cd broadcast && GOOS=$(GOOS) GOARCH=$(GOARCH) go build $(LDFLAGS) -o ../$(BIN_DIR)/broadcast$(SUFFIX) broadcast.go
	
	@echo "$(CYAN)  → Building ai_calibrator$(SUFFIX)...$(RESET)"
	@cd calibration && GOOS=$(GOOS) GOARCH=$(GOARCH) go build $(LDFLAGS) -o ../$(BIN_DIR)/ai_calibrator$(SUFFIX) main.go ai_calibrator.go
	
	@echo "$(CYAN)  → Building hand_calibrator$(SUFFIX)...$(RESET)"
	@cd calibration/hand_calibrator && GOOS=$(GOOS) GOARCH=$(GOARCH) go build $(LDFLAGS) -o ../../$(BIN_DIR)/hand_calibrator$(SUFFIX) main.go
	
	@echo "$(CYAN)  → Building pixelinches$(SUFFIX)...$(RESET)"
	@cd calibration/pixelinches && GOOS=$(GOOS) GOARCH=$(GOARCH) go build $(LDFLAGS) -o ../../$(BIN_DIR)/pixelinches$(SUFFIX) pixelinches.go
	
	@echo "$(CYAN)  → Building scanning_recorder$(SUFFIX)...$(RESET)"
	@cd calibration/scanning_recorder && GOOS=$(GOOS) GOARCH=$(GOARCH) go build $(LDFLAGS) -o ../../$(BIN_DIR)/scanning_recorder$(SUFFIX) main.go
	
	@echo "$(CYAN)  → Building search_calibrator$(SUFFIX)...$(RESET)"
	@cd calibration/search_calibrator && GOOS=$(GOOS) GOARCH=$(GOARCH) go build $(LDFLAGS) -o ../../$(BIN_DIR)/search_calibrator$(SUFFIX) main.go

# Development builds (current platform only)
dev: clean $(BIN_DIR)
	@echo "$(YELLOW)🔧 Building for current platform (development)...$(RESET)"
	@go build $(LDFLAGS) -o $(BIN_DIR)/NOLO NOLO.go
	@cd ai_commentary && go build $(LDFLAGS) -o ../$(BIN_DIR)/ai_commentary main.go
	@cd broadcast && go build $(LDFLAGS) -o ../$(BIN_DIR)/broadcast broadcast.go
	@cd calibration && go build $(LDFLAGS) -o ../$(BIN_DIR)/ai_calibrator main.go ai_calibrator.go
	@cd calibration/hand_calibrator && go build $(LDFLAGS) -o ../../$(BIN_DIR)/hand_calibrator main.go
	@cd calibration/pixelinches && go build $(LDFLAGS) -o ../../$(BIN_DIR)/pixelinches pixelinches.go
	@cd calibration/scanning_recorder && go build $(LDFLAGS) -o ../../$(BIN_DIR)/scanning_recorder main.go
	@cd calibration/search_calibrator && go build $(LDFLAGS) -o ../../$(BIN_DIR)/search_calibrator main.go
	@echo "$(GREEN)✅ Development build complete!$(RESET)"

# Individual binary targets for development
nolo: $(BIN_DIR)
	@echo "$(CYAN)Building NOLO...$(RESET)"
	@go build $(LDFLAGS) -o $(BIN_DIR)/NOLO NOLO.go

ai-commentary: $(BIN_DIR)
	@echo "$(CYAN)Building AI Commentary...$(RESET)"
	@cd ai_commentary && go build $(LDFLAGS) -o ../$(BIN_DIR)/ai_commentary main.go

broadcast: $(BIN_DIR)
	@echo "$(CYAN)Building Broadcast...$(RESET)"
	@cd broadcast && go build $(LDFLAGS) -o ../$(BIN_DIR)/broadcast broadcast.go

ai-calibrator: $(BIN_DIR)
	@echo "$(CYAN)Building AI Calibrator...$(RESET)"
	@cd calibration && go build $(LDFLAGS) -o ../$(BIN_DIR)/ai_calibrator main.go ai_calibrator.go

hand-calibrator: $(BIN_DIR)
	@echo "$(CYAN)Building Hand Calibrator...$(RESET)"
	@cd calibration/hand_calibrator && go build $(LDFLAGS) -o ../../$(BIN_DIR)/hand_calibrator main.go

pixelinches: $(BIN_DIR)
	@echo "$(CYAN)Building Pixelinches...$(RESET)"
	@cd calibration/pixelinches && go build $(LDFLAGS) -o ../../$(BIN_DIR)/pixelinches pixelinches.go

scanning-recorder: $(BIN_DIR)
	@echo "$(CYAN)Building Scanning Recorder...$(RESET)"
	@cd calibration/scanning_recorder && go build $(LDFLAGS) -o ../../$(BIN_DIR)/scanning_recorder main.go

search-calibrator: $(BIN_DIR)
	@echo "$(CYAN)Building Search Calibrator...$(RESET)"
	@cd calibration/search_calibrator && go build $(LDFLAGS) -o ../../$(BIN_DIR)/search_calibrator main.go

# Utility targets
clean:
	@echo "$(YELLOW)🧹 Cleaning up previous builds...$(RESET)"
	@rm -rf $(BIN_DIR)

test:
	@echo "$(CYAN)🧪 Running tests...$(RESET)"
	@go test ./...
	@cd ai_commentary && go test ./...
	@cd broadcast && go test ./...

deps:
	@echo "$(CYAN)📦 Downloading dependencies...$(RESET)"
	@go mod download
	@cd ai_commentary && go mod download 2>/dev/null || true
	@cd broadcast && go mod download

tidy:
	@echo "$(CYAN)🧹 Tidying go modules...$(RESET)"
	@go mod tidy
	@cd ai_commentary && go mod tidy 2>/dev/null || true
	@cd broadcast && go mod tidy

install-dev: dev
	@echo "$(CYAN)📦 Installing development binaries to /usr/local/bin...$(RESET)"
	@sudo cp $(BIN_DIR)/* /usr/local/bin/
	@echo "$(GREEN)✅ Binaries installed to /usr/local/bin$(RESET)"

# Package for distribution
package: all
	@echo "$(CYAN)📦 Creating distribution packages...$(RESET)"
	@mkdir -p dist
	@cd $(BIN_DIR) && tar -czf ../dist/rivercam-darwin-arm64.tar.gz *-darwin-arm64
	@cd $(BIN_DIR) && tar -czf ../dist/rivercam-linux-amd64.tar.gz *-linux-amd64
	@cd $(BIN_DIR) && tar -czf ../dist/rivercam-linux-arm64.tar.gz *-linux-arm64
	@echo "$(GREEN)✅ Distribution packages created in ./dist/$(RESET)"

help:
	@echo "$(CYAN)NOLO River Camera System - Build System$(RESET)"
	@echo ""
	@echo "$(YELLOW)⚠️  CROSS-COMPILATION NOTE:$(RESET)"
	@echo "  Cross-platform builds require OpenCV libraries for target platform."
	@echo "  Use 'dev' target for local development builds."
	@echo ""
	@echo "$(YELLOW)Main Targets:$(RESET)"
	@echo "  all          - Build all binaries for all platforms (default)"
	@echo "  dev          - Build all binaries for current platform only"
	@echo "  clean        - Remove all built binaries"
	@echo ""
	@echo "$(YELLOW)Platform-Specific Targets:$(RESET)"
	@echo "  darwin       - Build for macOS (Apple Silicon only)"
	@echo "  linux        - Build for Linux (x86_64 + ARM64)"
	@echo "  darwin-arm64 - Build for macOS Apple Silicon"
	@echo "  linux-amd64  - Build for Linux x86_64"
	@echo "  linux-arm64  - Build for Linux ARM64"
	@echo ""
	@echo "$(YELLOW)Individual Binary Targets (current platform):$(RESET)"
	@echo "  nolo              - Main NOLO application"
	@echo "  ai-commentary     - AI commentary service"
	@echo "  broadcast         - Broadcasting service"
	@echo "  ai-calibrator     - AI-powered calibration tool"
	@echo "  hand-calibrator   - Manual calibration tool"
	@echo "  pixelinches       - Pixel-to-inches calibrator"
	@echo "  scanning-recorder - Scanning pattern recorder"
	@echo "  search-calibrator - Search pattern calibrator"
	@echo ""
	@echo "$(YELLOW)Utility Targets:$(RESET)"
	@echo "  test         - Run all tests"
	@echo "  deps         - Download dependencies"
	@echo "  tidy         - Tidy go modules"
	@echo "  install-dev  - Install dev binaries to /usr/local/bin"
	@echo "  package      - Create distribution packages"
	@echo "  help         - Show this help message"
	@echo ""
	@echo "$(YELLOW)Output:$(RESET)"
	@echo "  Binaries: ./$(BIN_DIR)/"
	@echo "  Packages: ./dist/"
	@echo ""
	@echo "$(YELLOW)Examples:$(RESET)"
	@echo "  make           # Build everything"
	@echo "  make dev       # Quick development build"
	@echo "  make nolo      # Build just NOLO"
	@echo "  make darwin    # Build for macOS only"
	@echo "  make package   # Create distribution packages" 