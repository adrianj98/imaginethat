package drive

// SetReadSizes changes the read window sizes and returns a func restoring them.
func SetReadSizes(whole, window int64) func() {
	w, p := wholeFileMax, prefetchWindow
	wholeFileMax, prefetchWindow = whole, window
	return func() { wholeFileMax, prefetchWindow = w, p }
}

// SetUploadChunk changes the resumable chunk size and returns a restore func.
func SetUploadChunk(n int) func() {
	old := uploadChunk
	uploadChunk = n
	return func() { uploadChunk = old }
}
