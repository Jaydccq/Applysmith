package workspace

import "syscall"

// Filesystem magic numbers of the network filesystems (statfs(2)).
var networkMagic = map[int64]string{
	0x6969:     "nfs",
	0x517B:     "smbfs",
	0xFF534D42: "cifs",
	0xFE534D42: "smb2",
}

// filesystemType names a network filesystem holding path and returns "" for
// every other kind, or when it cannot be read.
func filesystemType(path string) string {
	var st syscall.Statfs_t
	if syscall.Statfs(path, &st) != nil {
		return ""
	}
	return networkMagic[int64(st.Type)&0xFFFFFFFF]
}
