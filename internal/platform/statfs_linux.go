package platform

import "syscall"

// diskSpace is the size and free space of the file system holding path, in
// bytes (0 if it can't be read).
func diskSpace(path string) (total, free uint64) {
	var st syscall.Statfs_t
	if syscall.Statfs(path, &st) != nil {
		return 0, 0
	}
	bs := uint64(st.Bsize)
	return st.Blocks * bs, st.Bavail * bs
}
