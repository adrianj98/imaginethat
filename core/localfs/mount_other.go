//go:build !darwin && !linux

package localfs

// IsNetworkMount is false where the platform gives no cheap way to tell.
func IsNetworkMount(dir string) bool { return false }
