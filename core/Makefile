.PHONY: all sync deps build test test-short vet fmt clean

# stupidgcm links OpenSSL unless this tag selects the Go stdlib crypto path.
GOTAGS  = without_openssl
GOFLAGS = -tags $(GOTAGS)

all: build

# Copy the gocryptfs internal/ packages into this module. Required once after
# cloning, and again whenever the pinned gocryptfs version changes.
sync:
	./scripts/sync-gocryptfs.sh

# internal/gocryptfs is generated, so make the build depend on it existing
# rather than failing with a confusing "no such package".
internal/gocryptfs:
	./scripts/sync-gocryptfs.sh

deps: internal/gocryptfs

build: deps
	go build $(GOFLAGS) ./vault/... ./mobile/... ./cache/... ./thumb/... ./xfer/... ./remote/... ./drive/...

test: deps
	go test $(GOFLAGS) ./vault/... ./mobile/... ./cache/... ./thumb/... ./xfer/... ./remote/... ./drive/...

# Skips the tests that need FUSE and the gocryptfs binary.
test-short: deps
	go test $(GOFLAGS) -short ./vault/... ./mobile/... ./cache/... ./thumb/... ./xfer/... ./remote/... ./drive/...

vet: deps
	go vet $(GOFLAGS) ./vault/... ./mobile/... ./cache/... ./thumb/... ./xfer/... ./remote/... ./drive/...

fmt:
	gofmt -w ./vault ./mobile ./cache ./thumb ./drive ./remote ./xfer

clean:
	go clean
