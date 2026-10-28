package rclonemount

import (
	"sync/atomic"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/rclone/rclone/fs/accounting"
	"github.com/rclone/rclone/fs/rc"

	"github.com/BlackDark/csi-driver-rclone/internal/metrics"
)

// Transfer totals: the Mount Pod's only rclone process is this mount, so rclone's
// global accounting is the volume's.
type transferStats struct {
	bytes, transfers, errors, checks, deletes int64
}

func globalTransfers() transferStats {
	s := accounting.GlobalStats()
	return transferStats{bytes: s.GetBytes(), transfers: s.GetTransfers(), errors: s.GetErrors(),
		checks: s.GetChecks(), deletes: s.GetDeletes()}
}

// vfsStats are the numbers taken from vfs.Stats(); cache fields are zero in cache mode off.
type vfsStats struct {
	dirs, files                            int
	cacheBytes                             int64
	cacheFiles, errored, uploading, queued int
	outOfSpace, hasCache                   bool
}

func parseVFSStats(p rc.Params) vfsStats {
	var out vfsStats
	if md, ok := p["metadataCache"].(rc.Params); ok {
		out.dirs, _ = md["dirs"].(int)
		out.files, _ = md["files"].(int)
	}
	dc, ok := p["diskCache"].(rc.Params)
	if !ok {
		return out
	}
	out.hasCache = true
	out.cacheBytes, _ = dc["bytesUsed"].(int64)
	out.cacheFiles, _ = dc["files"].(int)
	out.errored, _ = dc["erroredFiles"].(int)
	out.uploading, _ = dc["uploadsInProgress"].(int)
	out.queued, _ = dc["uploadsQueued"].(int)
	out.outOfSpace, _ = dc["outOfSpace"].(bool)
	return out
}

func desc(name, help string) *prometheus.Desc {
	return prometheus.NewDesc(metrics.Namespace+"_"+name, help, []string{"volume_id"}, nil)
}

var (
	descBytes     = desc("transferred_bytes_total", "Bytes moved to and from the remote.")
	descTransfers = desc("transfers_total", "Completed file transfers.")
	descErrors    = desc("transfer_errors_total", "rclone errors.")
	descChecks    = desc("checks_total", "rclone file checks.")
	descDeletes   = desc("deletes_total", "Files deleted on the remote.")
	descDirs      = desc("vfs_dir_cache_dirs", "Directories in the VFS directory cache.")
	descFiles     = desc("vfs_dir_cache_files", "Files in the VFS directory cache.")
	descCacheB    = desc("vfs_cache_bytes", "Bytes used by the VFS Cache on disk.")
	descCacheF    = desc("vfs_cache_files", "Files in the VFS Cache.")
	descErrored   = desc("vfs_cache_errored_files", "VFS Cache files whose upload failed.")
	descUploading = desc("vfs_uploads_in_progress", "Uploads running from the VFS Cache.")
	descQueued    = desc("vfs_uploads_queued", "Uploads waiting in the VFS Cache.")
	descOOS       = desc("vfs_cache_out_of_space", "1 when the VFS Cache ran out of space.")
)

// collector reports one Mount Pod's stats at scrape time. vfs is nil until mounted.
type collector struct {
	volumeID  string
	transfers func() transferStats
	vfs       atomic.Pointer[func() rc.Params]
}

func newCollector(volumeID string, transfers func() transferStats) *collector {
	return &collector{volumeID: volumeID, transfers: transfers}
}

func (c *collector) setVFS(f func() rc.Params) { c.vfs.Store(&f) }

func (c *collector) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range []*prometheus.Desc{descBytes, descTransfers, descErrors, descChecks, descDeletes,
		descDirs, descFiles, descCacheB, descCacheF, descErrored, descUploading, descQueued, descOOS} {
		ch <- d
	}
}

func (c *collector) Collect(ch chan<- prometheus.Metric) {
	emit := func(d *prometheus.Desc, t prometheus.ValueType, v float64) {
		ch <- prometheus.MustNewConstMetric(d, t, v, c.volumeID)
	}
	ts := c.transfers()
	emit(descBytes, prometheus.CounterValue, float64(ts.bytes))
	emit(descTransfers, prometheus.CounterValue, float64(ts.transfers))
	emit(descErrors, prometheus.CounterValue, float64(ts.errors))
	emit(descChecks, prometheus.CounterValue, float64(ts.checks))
	emit(descDeletes, prometheus.CounterValue, float64(ts.deletes))
	f := c.vfs.Load()
	if f == nil {
		return
	}
	vs := parseVFSStats((*f)())
	emit(descDirs, prometheus.GaugeValue, float64(vs.dirs))
	emit(descFiles, prometheus.GaugeValue, float64(vs.files))
	if !vs.hasCache {
		return
	}
	emit(descCacheB, prometheus.GaugeValue, float64(vs.cacheBytes))
	emit(descCacheF, prometheus.GaugeValue, float64(vs.cacheFiles))
	emit(descErrored, prometheus.GaugeValue, float64(vs.errored))
	emit(descUploading, prometheus.GaugeValue, float64(vs.uploading))
	emit(descQueued, prometheus.GaugeValue, float64(vs.queued))
	oos := 0.0
	if vs.outOfSpace {
		oos = 1
	}
	emit(descOOS, prometheus.GaugeValue, oos)
}
