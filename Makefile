APP_NAME ?= caddy-rulefiles

# try getconf (linux / macos), getconf (BSD), nproc, then fallback to 1
NPROCS := $(shell getconf _NPROCESSORS_ONLN 2>/dev/null || getconf NPROCESSORS_ONLN 2>/dev/null || nproc 2>/dev/null || echo 1)
MAKEFLAGS += --jobs=$(NPROCS)

.PHONY: all build test clean lint lint-fix mod-tidy vet golangci-lint staticcheck modernize govulncheck typos update help

all: build

build:
	go build ./...

test:
	go test -race ./...

lint: mod-tidy vet staticcheck golangci-lint modernize govulncheck typos

lint-fix:
	go mod tidy
	golangci-lint run --fix
	go fix ./...
	typos -w
	$(MAKE) lint

mod-tidy:
	go mod tidy -diff

vet:
	go vet ./...

golangci-lint:
	golangci-lint config verify
	golangci-lint run

staticcheck:
	go run honnef.co/go/tools/cmd/staticcheck@latest ./...

modernize:
	go fix -diff ./...

govulncheck:
	go run golang.org/x/vuln/cmd/govulncheck@latest ./...

typos:
	typos

update:
	go get -v -u ./...
	# cel-go >= v0.29 changes interpreter.NewCall to take []InterpretableV2,
	# which caddy v2.11.4 does not compile against. Drop once caddy catches up.
	go get github.com/google/cel-go@v0.28.1
	# automemlimit v1.0.0 renames SetGoMemLimitWithOpts to Set, which caddy
	# v2.11.4 does not compile against. Drop once caddy catches up.
	go get github.com/KimMachineGun/automemlimit@v0.7.5
	go mod tidy
	$(MAKE) test

clean:
	rm -rf dist/

help:
	@echo "make           # Build $(APP_NAME)"
	@echo "make build     # Build $(APP_NAME) snapshot binary"
	@echo "make test      # Run tests"
	@echo "make lint-fix  # Run linters and try fix issues"
	@echo "make lint      # Run linters"
	@echo "make typos     # Run typo checker"
	@echo "make update    # Update dependencies"
	@echo "make clean     # Remove built app"
	@echo "make help      # Show this help"
