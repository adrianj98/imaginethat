package localfs

import "golang.org/x/sys/unix"

// Filesystem magic numbers of network filesystems (statfs f_type).
var networkTypes = map[int64]bool{
	0x6969:     true, // NFS
	0x517b:     true, // SMB
	0xff534d42: true, // CIFS
	0xfe534d42: true, // SMB2
	0x01021997: true, // 9P
	0x00c36400: true, // Ceph
	0x5346414f: true, // AFS
}

// IsNetworkMount reports whether dir is on a network filesystem.
func IsNetworkMount(dir string) bool {
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return false
	}
	return networkTypes[int64(st.Type)]
}
