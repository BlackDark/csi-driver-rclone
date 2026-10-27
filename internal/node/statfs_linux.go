//go:build linux

package node

import "golang.org/x/sys/unix"

// Statfs runs statfs(2) on path; a dead FUSE mount fails with ENOTCONN.
func Statfs(path string) (Usage, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return Usage{}, err
	}
	bs := st.Bsize
	return Usage{
		TotalBytes: int64(st.Blocks) * bs, AvailableBytes: int64(st.Bavail) * bs,
		UsedBytes:   int64(st.Blocks-st.Bfree) * bs,
		TotalInodes: int64(st.Files), FreeInodes: int64(st.Ffree), UsedInodes: int64(st.Files - st.Ffree),
	}, nil
}
