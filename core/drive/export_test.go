package drive

import "github.com/adrianm/imagethat/core/cache"

// SetReadSizes changes the read window sizes and returns a func restoring them.
func SetReadSizes(whole, window int64) func() {
	w, p := cache.WholeFileMax, cache.Window
	cache.WholeFileMax, cache.Window = whole, window
	return func() { cache.WholeFileMax, cache.Window = w, p }
}

// SetUploadChunk changes the resumable chunk size and returns a restore func.
func SetUploadChunk(n int) func() {
	old := uploadChunk
	uploadChunk = n
	return func() { uploadChunk = old }
}
