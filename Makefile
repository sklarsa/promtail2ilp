# Promtail2ILP Makefile

# Variables
BINARY_NAME=promtail2ilp
GO_FILES=$(shell find . -name "*.go" -type f)
VERSION=$(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
BUILD_TIME=$(shell date -u '+%Y-%m-%d %H:%M:%S UTC')
LDFLAGS=-ldflags "-X 'main.Version=$(VERSION)' -X 'main.BuildTime=$(BUILD_TIME)'"

# Default target
.DEFAULT_GOAL := build

# Build the binary
.PHONY: build
build:
	@echo "Building $(BINARY_NAME)..."
	go build $(LDFLAGS) -o $(BINARY_NAME) .

# Run unit and server tests with coverage (excludes integration and stress)
.PHONY: test
test:
	@echo "Running unit and server tests with coverage..."
	go test -v -coverprofile=coverage.out ./server
	@echo "Coverage report:"
	@go tool cover -func=coverage.out

# Run integration tests
.PHONY: integration
integration:
	@echo "Running integration tests..."
	go test -v -timeout=30m ./integration

# Run stress tests
.PHONY: stress
stress:
	@echo "Running all stress tests..."
	go test -v -timeout=30m ./stress

# Run all tests (unit, integration, stress)
.PHONY: test-all
test-all: test integration stress
	@echo "All tests completed!"


# Run the application
.PHONY: run
run: build
	@echo "Running $(BINARY_NAME)..."
	./$(BINARY_NAME)

# Run with debug logging
.PHONY: run-debug
run-debug: build
	@echo "Running $(BINARY_NAME) with debug logging..."
	./$(BINARY_NAME) -log-level debug

# Run with trace logging
.PHONY: run-trace
run-trace: build
	@echo "Running $(BINARY_NAME) with trace logging..."
	./$(BINARY_NAME) -log-level trace

# Clean build artifacts
.PHONY: clean
clean:
	@echo "Cleaning..."
	rm -f $(BINARY_NAME)
	rm -f coverage.out

# Format code
.PHONY: fmt
fmt:
	@echo "Formatting code..."
	go fmt ./...

# Lint code with golangci-lint
.PHONY: lint
lint: install-golangci-lint
	@echo "Running go vet..."
	go vet ./...
	@echo "Running golangci-lint..."
	golangci-lint run

# Install golangci-lint
.PHONY: install-golangci-lint
install-golangci-lint:
	@echo "Installing golangci-lint..."
	@if ! command -v golangci-lint >/dev/null 2>&1; then \
		curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b $$(go env GOPATH)/bin; \
	else \
		echo "golangci-lint is already installed"; \
	fi

# Tidy dependencies
.PHONY: tidy
tidy:
	@echo "Tidying dependencies..."
	go mod tidy

# Run all checks (format, lint, test)
.PHONY: check
check: fmt lint test
	@echo "All checks passed!"

# Show help
.PHONY: help
help:
	@echo "Available targets:"
	@echo "  build                Build the binary"
	@echo "  test                 Run unit and server tests with coverage"
	@echo "  integration          Run integration tests (QuestDB, containers)"
	@echo "  stress               Run all stress tests (extreme load testing)"
	@echo "  test-all             Run all tests (unit + integration + stress)"
	@echo "  check                Run all checks (fmt, lint, test)"
	@echo "  run                  Build and run the application"
	@echo "  run-debug            Build and run with debug logging"
	@echo "  run-trace            Build and run with trace logging"
	@echo "  clean                Clean build artifacts"
	@echo "  fmt                  Format code"
	@echo "  lint                 Run golangci-lint"
	@echo "  tidy                 Tidy dependencies"
	@echo "  help                 Show this help message"