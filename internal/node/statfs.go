package node

// Usage is a target's filesystem usage as statfs(2) reports it.
type Usage struct {
	TotalBytes, AvailableBytes, UsedBytes int64
	TotalInodes, FreeInodes, UsedInodes   int64
}
