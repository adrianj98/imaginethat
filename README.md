# ImageThat

macOS image/video explorer with gocryptfs encrypted vault support. Decrypted data never touches disk.

## Build & Run

Requires macOS 14+, Go, and Swift.

```
  make build    # compiles Go lib + Swift app
  make run      # builds + launches the app                                                                                                    
  make clean    # wipe all build artifacts
```

## Shortcuts

- **Cmd+O** — open folder
- **Cmd+Shift+O** — open encrypted vault
- **Arrow keys** — navigate images
- **Escape** — back to grid
- **Spacebar** — play/pause video
