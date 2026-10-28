package node

import (
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/BlackDark/csi-driver-rclone/internal/handoff"
)

func (h *harness) gather(t *testing.T, want string, names ...string) {
	t.Helper()
	reg := prometheus.NewRegistry()
	reg.MustRegister(h.srv.Collector())
	if err := testutil.GatherAndCompare(reg, strings.NewReader(want), names...); err != nil {
		t.Fatal(err)
	}
}

func podsWant(ready, lingering, stopping int) string {
	return `
# HELP csi_rclone_mount_pods Mount Pods on this node by state.
# TYPE csi_rclone_mount_pods gauge
csi_rclone_mount_pods{state="lingering"} ` + strconv.Itoa(lingering) + `
csi_rclone_mount_pods{state="ready"} ` + strconv.Itoa(ready) + `
csi_rclone_mount_pods{state="starting"} ` + "0" + `
csi_rclone_mount_pods{state="stopping"} ` + strconv.Itoa(stopping) + `
`
}

func TestMetricsFollowMountLifecycle(t *testing.T) {
	h := newHarness(t)
	h.srv.deps.Metrics = NewMetrics(prometheus.NewRegistry())
	h.srv.cfg.Linger = time.Minute
	h.gather(t, podsWant(0, 0, 0), "csi_rclone_mount_pods")

	a := filepath.Join(h.dir, "a")
	h.publish(t, a)
	id := h.srv.firstID()
	h.gather(t, podsWant(1, 0, 0)+`
# HELP csi_rclone_mount_healthy 1 when the Mount Pod serves its mount, 0 while starting or after it was lost.
# TYPE csi_rclone_mount_healthy gauge
csi_rclone_mount_healthy{id="`+id+`",volume_id="vol-1"} 1
# HELP csi_rclone_mount_pod_info One per Mount Pod on this node, with its image.
# TYPE csi_rclone_mount_pod_info gauge
csi_rclone_mount_pod_info{id="`+id+`",image="img",volume_id="vol-1"} 1
`, "csi_rclone_mount_pods", "csi_rclone_mount_healthy", "csi_rclone_mount_pod_info")

	h.unpublish(t, a)
	h.gather(t, podsWant(0, 1, 0), "csi_rclone_mount_pods")

	h.clk.Step(time.Minute)
	h.gather(t, podsWant(0, 0, 1), "csi_rclone_mount_pods")
	h.ho.send(handoff.Event{Kind: handoff.EventDrainTimeout, Conn: 1})
	h.srv.Wait()
	h.gather(t, podsWant(0, 0, 0), "csi_rclone_mount_pods")
	if v := testutil.ToFloat64(h.srv.deps.Metrics.drain.WithLabelValues(DrainTimeout)); v != 1 {
		t.Fatalf("drain timeout = %v", v)
	}
	if v := testutil.ToFloat64(h.srv.deps.Metrics.drain.WithLabelValues(DrainDrained)); v != 0 {
		t.Fatalf("drained = %v", v)
	}
}

func TestMetricsCountLostMount(t *testing.T) {
	h := newHarness(t)
	h.publish(t, filepath.Join(h.dir, "a"))
	h.ho.send(handoff.Event{Kind: handoff.EventClosed, Conn: 1})
	eventually(t, "recreated Mount Pod", func() bool { return h.ho.serveCount() == 2 })
	h.srv.Wait()
	if v := testutil.ToFloat64(h.srv.deps.Metrics.lost); v != 1 {
		t.Fatalf("lost = %v", v)
	}
}

func TestCollectMountsUnhealthyAndStarting(t *testing.T) {
	ch := make(chan prometheus.Metric, 16)
	collectMounts([]mountSample{{id: "x", volumeID: "v", image: "i", state: StateStarting}}, ch)
	close(ch)
	n := 0
	for range ch {
		n++
	}
	if n != 2+len(mountStates) {
		t.Fatalf("series = %d", n)
	}
}
