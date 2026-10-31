package localfs

import "golang.org/x/sys/unix"

// IsNetworkMount reports whether dir is on a filesystem that is not local
// (smbfs, nfs, afpfs, webdav): the kernel marks local ones MNT_LOCAL.
func IsNetworkMount(dir string) bool {
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return false
	}
	return st.Flags&unix.MNT_LOCAL == 0
}
