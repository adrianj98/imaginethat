.PHONY: build run golib clean app test test-logic test-e2e test-fixtures \
        web-build web-all web-run web-test

BIN_DIR = $(shell swift build --show-bin-path)
APP_BUNDLE = build/ImageThat.app

# Build everything
build: golib
	swift build

# Create .app bundle and launch it
# Usage: make run [FOLDER=/path/to/folder]
run: app
ifdef FOLDER
	open -n $(APP_BUNDLE) --args "$(FOLDER)"
else
	open $(APP_BUNDLE)
endif

# Build the .app bundle
app: build
	mkdir -p $(APP_BUNDLE)/Contents/MacOS
	mkdir -p $(APP_BUNDLE)/Contents/Frameworks
	cp $(BIN_DIR)/ImageThat $(APP_BUNDLE)/Contents/MacOS/
	cp build/libgocryptfs.dylib $(APP_BUNDLE)/Contents/Frameworks/
	install_name_tool -change libgocryptfs.dylib @executable_path/../Frameworks/libgocryptfs.dylib $(APP_BUNDLE)/Contents/MacOS/ImageThat
	codesign --force --sign - $(APP_BUNDLE)/Contents/Frameworks/libgocryptfs.dylib
	codesign --force --sign - $(APP_BUNDLE)/Contents/MacOS/ImageThat
	cp ImageThat/Resources/Info.plist $(APP_BUNDLE)/Contents/Info.plist
	codesign --force --sign - $(APP_BUNDLE)

# Build the Go shared library
golib:
	cd gocryptfs-lib && CGO_ENABLED=1 GOOS=darwin GOARCH=arm64 \
		go build -tags without_openssl -buildmode=c-shared \
		-o ../build/libgocryptfs.dylib .

# --- Go webserver ---------------------------------------------------------
# A standalone HTTP server that browses the same gocryptfs vaults from a
# browser. It is a separate Go module under webserver/ and shares no build
# state with the macOS app.

# Build the webserver binary (webserver/bin/imagethat-web)
web-build:
	$(MAKE) -C webserver build

# Build the browser UI, then the binary that embeds it
web-all:
	$(MAKE) -C webserver web build

# Run the webserver. Usage: make web-run ROOT=/path/to/vaults
web-run:
	$(MAKE) -C webserver run ROOT="$(ROOT)"

# Run the webserver's Go tests
web-test:
	$(MAKE) -C webserver test

# Run all tests
test: test-logic test-e2e

# Copy the committed fixtures to the /tmp paths IntegrationTest reads.
# The vault was made with the real gocryptfs (password: testpass123).
test-fixtures:
	rm -rf /tmp/imagethat-test-plain /tmp/imagethat-test
	mkdir -p /tmp/imagethat-test
	cp -R Tests/fixtures/plain /tmp/imagethat-test-plain
	cp -R Tests/fixtures/vault /tmp/imagethat-test/vault

# Run logic/integration tests
test-logic: build test-fixtures
	DYLD_LIBRARY_PATH=$(PWD)/build $(BIN_DIR)/IntegrationTest

# Run E2E tests (needs Screen Recording permission for the terminal)
test-e2e: app test-fixtures
	bash Tests/e2e_test.sh

# Clean all build artifacts
clean:
	rm -rf .build build
	cd gocryptfs-lib && go clean
