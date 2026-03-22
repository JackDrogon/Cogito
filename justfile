#!/usr/bin/env -S just --justfile

# Justfile for Cogito
# Run `just` or `just --list` to see available recipes

tag := `root="$(git rev-parse --show-toplevel 2>/dev/null || true)"; current="$(pwd -P)"; if [ -n "$root" ] && [ "$root" = "$current" ]; then git describe --abbrev=0 --always --tags 2>/dev/null || echo dev; else echo dev; fi`
ldflags := "-X 'github.com/JackDrogon/Cogito/internal/version.Version=" + tag + "'"

# ─────────────────────────────────────────────────────────────────────
# Aliases
# ─────────────────────────────────────────────────────────────────────

alias b := build
alias c := cover
alias t := test
alias l := lint
alias f := fmt
alias r := run
alias pc := pre-commit
alias rs := release-snapshot

# Show all available recipes
[private]
default:
    @just --list --unsorted

# ═════════════════════════════════════════════════════════════════════
#  Build
# ═════════════════════════════════════════════════════════════════════

# Build binary to bin/cogito
[group('build')]
build:
    @mkdir -p bin
    go build -ldflags "{{ldflags}}" -o bin/cogito ./cmd/cogito

# Install the CLI into GOPATH/bin
[group('build')]
install:
    go install -ldflags "{{ldflags}}" ./cmd/cogito

# Remove build artifacts
[group('build')]
clean:
    rm -rf bin

# ═════════════════════════════════════════════════════════════════════
#  Code Quality
# ═════════════════════════════════════════════════════════════════════

# Run golangci-lint
[group('quality')]
lint:
    golangci-lint run

# Format all Go code
[group('quality')]
fmt:
    gofumpt -l -w .

# Tidy module dependencies
[group('quality')]
tidy:
    go mod tidy

# ═════════════════════════════════════════════════════════════════════
#  Test
# ═════════════════════════════════════════════════════════════════════

# Run tests
[group('test')]
test pkg='./...':
    go test {{pkg}}

# Run tests with verbose output
[group('test')]
test-v pkg='./...':
    go test -v {{pkg}}

# Generate coverage report
[group('test')]
cover:
    go test -coverprofile=coverage.out ./...
    go tool cover -func=coverage.out

# Run formatting, linting, and tests in sequence
[group('test')]
pre-commit: fmt lint test

# Build release artifacts without publishing
[group('test')]
release-snapshot:
    goreleaser release --snapshot --clean

# ═════════════════════════════════════════════════════════════════════
#  Run
# ═════════════════════════════════════════════════════════════════════

# Build and run (e.g., just run -- --help)
[group('run')]
run *args: build
    ./bin/cogito {{args}}

# ═════════════════════════════════════════════════════════════════════
#  Maintenance
# ═════════════════════════════════════════════════════════════════════

# Count lines of code (requires tokei)
[group('maintenance')]
loc:
    tokei --sort code

# Print all TODOs in codebase
[group('maintenance')]
todos:
    grep -rnw . -e "TODO" | grep -v '^./.git'

# Show concise git log
[group('maintenance')]
log n='20':
    git log --oneline --graph --decorate -n {{n}}

# Show Go toolchain info
[group('maintenance')]
info:
    @echo "── Go Toolchain ──"
    go version
    @echo ""
    @echo "── Module ──"
    head -1 go.mod
