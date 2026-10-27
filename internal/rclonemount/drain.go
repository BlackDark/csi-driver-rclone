package rclonemount

import (
	"context"
	"time"

	"github.com/rclone/rclone/fs/rc"
)

// pendingUploads reads the VFS cache upload counters from vfs.Stats(). No disk cache
// (cache mode off) means writes were synchronous and nothing is pending.
func pendingUploads(stats rc.Params) int {
	cache, ok := stats["diskCache"].(rc.Params)
	if !ok {
		return 0
	}
	inProgress, _ := cache["uploadsInProgress"].(int)
	queued, _ := cache["uploadsQueued"].(int)
	return inProgress + queued
}

// drain polls stats until no uploads are pending; false means the timeout hit first.
func drain(ctx context.Context, stats func() rc.Params, timeout, poll time.Duration) bool {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	t := time.NewTicker(poll)
	defer t.Stop()
	for {
		if pendingUploads(stats()) == 0 {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-t.C:
		}
	}
}
