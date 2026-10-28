package rclonemount

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/rclone/rclone/fs/rc"
)

func TestCollectorBeforeMountHasTransfersOnly(t *testing.T) {
	c := newCollector("vol-1", func() transferStats { return transferStats{bytes: 42, transfers: 3, errors: 1} })
	if n := testutil.CollectAndCount(c); n != 5 {
		t.Fatalf("series = %d, want 5", n)
	}
	want := `
# HELP csi_rclone_transferred_bytes_total Bytes moved to and from the remote.
# TYPE csi_rclone_transferred_bytes_total counter
csi_rclone_transferred_bytes_total{volume_id="vol-1"} 42
# HELP csi_rclone_transfer_errors_total rclone errors.
# TYPE csi_rclone_transfer_errors_total counter
csi_rclone_transfer_errors_total{volume_id="vol-1"} 1
`
	if err := testutil.CollectAndCompare(c, strings.NewReader(want),
		"csi_rclone_transferred_bytes_total", "csi_rclone_transfer_errors_total"); err != nil {
		t.Fatal(err)
	}
}

func TestCollectorReadsVFSStats(t *testing.T) {
	c := newCollector("vol-1", func() transferStats { return transferStats{} })
	c.setVFS(func() rc.Params {
		return rc.Params{
			"metadataCache": rc.Params{"dirs": 2, "files": 7},
			"diskCache": rc.Params{"bytesUsed": int64(1024), "files": 4, "erroredFiles": 1,
				"uploadsInProgress": 1, "uploadsQueued": 3, "outOfSpace": true},
		}
	})
	reg := prometheus.NewRegistry()
	reg.MustRegister(c)
	want := `
# HELP csi_rclone_vfs_cache_bytes Bytes used by the VFS Cache on disk.
# TYPE csi_rclone_vfs_cache_bytes gauge
csi_rclone_vfs_cache_bytes{volume_id="vol-1"} 1024
# HELP csi_rclone_vfs_cache_out_of_space 1 when the VFS Cache ran out of space.
# TYPE csi_rclone_vfs_cache_out_of_space gauge
csi_rclone_vfs_cache_out_of_space{volume_id="vol-1"} 1
# HELP csi_rclone_vfs_dir_cache_files Files in the VFS directory cache.
# TYPE csi_rclone_vfs_dir_cache_files gauge
csi_rclone_vfs_dir_cache_files{volume_id="vol-1"} 7
# HELP csi_rclone_vfs_uploads_queued Uploads waiting in the VFS Cache.
# TYPE csi_rclone_vfs_uploads_queued gauge
csi_rclone_vfs_uploads_queued{volume_id="vol-1"} 3
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(want), "csi_rclone_vfs_cache_bytes",
		"csi_rclone_vfs_cache_out_of_space", "csi_rclone_vfs_dir_cache_files", "csi_rclone_vfs_uploads_queued"); err != nil {
		t.Fatal(err)
	}
	if n := testutil.CollectAndCount(c); n != 13 {
		t.Fatalf("series = %d, want 13", n)
	}
}

func TestCollectorCacheModeOffOmitsCacheSeries(t *testing.T) {
	c := newCollector("v", func() transferStats { return transferStats{} })
	c.setVFS(func() rc.Params { return rc.Params{"metadataCache": rc.Params{"dirs": 1, "files": 1}} })
	if n := testutil.CollectAndCount(c, "csi_rclone_vfs_cache_bytes"); n != 0 {
		t.Fatalf("cache series without a cache: %d", n)
	}
}
