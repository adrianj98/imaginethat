.PHONY: all sync web build run test test-short vet fmt clean deps

# stupidgcm links OpenSSL unless this tag selects the Go stdlib crypto path.
GOTAGS  = without_openssl
GOFLAGS = -tags $(GOTAGS)
BIN     = bin/imagethat-web

# Where to look for vaults when running locally.
ROOT ?= .
ADDR ?= 127.0.0.1:8347

all: build

# The vault engine lives in ../core, which carries the generated copy of the
# gocryptfs internal/ packages. Required once after cloning, and again
# whenever the pinned gocryptfs version changes.
sync:
	$(MAKE) -C ../core sync

../core/internal/gocryptfs:
	$(MAKE) -C ../core sync

deps: ../core/internal/gocryptfs

# Build the SPA into server/dist, where go:embed picks it up.
web:
	cd web && npm ci && npm run build

build: deps
	go build $(GOFLAGS) -o $(BIN) ./cmd/imagethat-web

run: build
	./$(BIN) -root "$(ROOT)" -addr "$(ADDR)"

test: deps
	go test $(GOFLAGS) ./server/...

# Skips the tests that need FUSE and the gocryptfs binary.
test-short: deps
	go test $(GOFLAGS) -short ./server/...

vet: deps
	go vet $(GOFLAGS) ./server/... ./cmd/...

fmt:
	gofmt -w ./server ./cmd

clean:
	rm -rf bin server/dist/assets server/dist/index.html web/node_modules
	go clean
