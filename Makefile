.PHONY: build run golib clean app test test-logic test-e2e

BIN_DIR = .build/arm64-apple-macosx/debug
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
	codesign --force --sign - $(APP_BUNDLE)
	@echo '<?xml version="1.0" encoding="UTF-8"?>\n\
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">\n\
<plist version="1.0"><dict>\n\
<key>CFBundleName</key><string>ImageThat</string>\n\
<key>CFBundleIdentifier</key><string>com.adrianm.imagethat</string>\n\
<key>CFBundleVersion</key><string>1.0</string>\n\
<key>CFBundleExecutable</key><string>ImageThat</string>\n\
<key>CFBundlePackageType</key><string>APPL</string>\n\
<key>NSHighResolutionCapable</key><true/>\n\
<key>LSMinimumSystemVersion</key><string>14.0</string>\n\
</dict></plist>' > $(APP_BUNDLE)/Contents/Info.plist

# Build the Go shared library
golib:
	cd gocryptfs-lib && CGO_ENABLED=1 GOOS=darwin GOARCH=arm64 \
		go build -tags without_openssl -buildmode=c-shared \
		-o ../build/libgocryptfs.dylib .

# Run all tests
test: test-logic test-e2e

# Run logic/integration tests
test-logic: build
	DYLD_LIBRARY_PATH=$(PWD)/build $(BIN_DIR)/IntegrationTest

# Run E2E tests (requires Peekaboo: brew install steipete/tap/peekaboo)
test-e2e: app
	bash Tests/e2e_test.sh

# Clean all build artifacts
clean:
	rm -rf .build build
	cd gocryptfs-lib && go clean
