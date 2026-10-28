//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

var (
	nodeSeries = []string{
		"csi_rclone_requests_total", "csi_rclone_request_duration_seconds_bucket", "csi_rclone_mount_pods",
		"csi_rclone_mount_pod_info", "csi_rclone_mount_healthy", "csi_rclone_drain_total",
		"csi_rclone_mount_lost_total", "csi_rclone_adoptions_total", "csi_rclone_workload_heals_total",
	}
	controllerSeries = []string{"csi_rclone_requests_total", "csi_rclone_request_duration_seconds_bucket"}
	mountSeries      = []string{
		"csi_rclone_transferred_bytes_total", "csi_rclone_transfers_total", "csi_rclone_transfer_errors_total",
		"csi_rclone_checks_total", "csi_rclone_deletes_total", "csi_rclone_vfs_dir_cache_dirs",
		"csi_rclone_vfs_dir_cache_files", "csi_rclone_vfs_cache_bytes", "csi_rclone_vfs_cache_files",
		"csi_rclone_vfs_cache_errored_files", "csi_rclone_vfs_uploads_in_progress",
		"csi_rclone_vfs_uploads_queued", "csi_rclone_vfs_cache_out_of_space",
	}
)

func TestMetricsEndpointsAndProbes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	cs := client(t)
	setupNamespace(ctx, t, cs)
	vol := "e2e-metrics"
	createVolume(ctx, t, cs, staticPV(vol, map[string]string{
		"remote": "minio", "remotePath": "e2e-bucket/metrics", "vfs-cache-mode": "writes",
	}))
	pod := workload("e2e-metrics", vol, "echo m > /data/m.txt && cat /data/m.txt && echo METRICS-OK && sleep 3600")
	mustCreatePod(ctx, t, cs, pod)
	waitLogContains(ctx, t, cs, appNS, pod.Name, "METRICS-OK")

	mp := soleMountPod(ctx, t, cs)
	poll(ctx, t, "Mount Pod ready", func() (bool, error) {
		p, err := cs.CoreV1().Pods(driverNS()).Get(ctx, mp.Name, metav1.GetOptions{})
		return err == nil && podReady(p), err
	})
	mount := scrape(ctx, t, cs, mp.Name, "9810", "/metrics")
	requireSeries(t, "mount pod", mount, mountSeries)
	if !strings.Contains(mount, `volume_id="`+vol+`"`) {
		t.Errorf("mount pod series not labelled with volume %s", vol)
	}
	if h := scrape(ctx, t, cs, mp.Name, "9810", "/healthz"); h != "ok" {
		t.Errorf("mount pod /healthz = %q", h)
	}

	node := scrape(ctx, t, cs, componentPod(ctx, t, cs, "node", mp.Spec.NodeName), "9809", "/metrics")
	requireSeries(t, "node plugin", node, nodeSeries)
	healthy := regexp.MustCompile(`csi_rclone_mount_healthy\{[^}]*volume_id="` + vol + `"[^}]*\} 1`)
	if !healthy.MatchString(node) {
		t.Errorf("no healthy gauge for %s", vol)
	}
	if !strings.Contains(node, `method="/csi.v1.Node/NodePublishVolume"`) {
		t.Error("NodePublishVolume not counted")
	}
	ctrl := scrape(ctx, t, cs, componentPod(ctx, t, cs, "controller", ""), "9809", "/metrics")
	requireSeries(t, "controller", ctrl, controllerSeries)

	requireDashboardSeries(t, node+ctrl+mount)
	requireProbes(ctx, t, cs, mp)

	deletePod(ctx, t, cs, pod.Name)
	poll(ctx, t, "Mount Pod gone", func() (bool, error) { return countMount(ctx, t, cs) == 0, nil })
}

func podReady(p *corev1.Pod) bool {
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

// scrape goes through the API server's pod proxy, so the test needs no port-forward.
func scrape(ctx context.Context, t *testing.T, cs kubernetes.Interface, pod, port, path string) string {
	t.Helper()
	b, err := cs.CoreV1().Pods(driverNS()).ProxyGet("http", pod, port, path, nil).DoRaw(ctx)
	if err != nil {
		t.Fatalf("scrape %s:%s%s: %v", pod, port, path, err)
	}
	return string(b)
}

func componentPod(ctx context.Context, t *testing.T, cs kubernetes.Interface, component, nodeName string) string {
	t.Helper()
	l, err := cs.CoreV1().Pods(driverNS()).List(ctx, metav1.ListOptions{
		LabelSelector: "app.kubernetes.io/name=csi-driver-rclone,app.kubernetes.io/component=" + component,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range l.Items {
		if p.Status.Phase == corev1.PodRunning && p.DeletionTimestamp == nil && (nodeName == "" || p.Spec.NodeName == nodeName) {
			return p.Name
		}
	}
	t.Fatalf("no running %s pod on %q", component, nodeName)
	return ""
}

func requireSeries(t *testing.T, what, body string, names []string) {
	t.Helper()
	for _, n := range names {
		if !strings.Contains(body, "\n"+n+"{") && !strings.Contains(body, "\n"+n+" ") {
			t.Errorf("%s: series %s missing", what, n)
		}
	}
}

// requireDashboardSeries: every series a dashboard panel queries is served, so each
// panel renders data against this cluster.
func requireDashboardSeries(t *testing.T, scraped string) {
	t.Helper()
	raw, err := os.ReadFile("../../charts/csi-driver-rclone/dashboards/csi-driver-rclone.json")
	if err != nil {
		t.Fatal(err)
	}
	var d struct {
		Panels []struct {
			Title   string                  `json:"title"`
			Targets []struct{ Expr string } `json:"targets"`
		} `json:"panels"`
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`csi_rclone_[a-z_]+`)
	for _, p := range d.Panels {
		for _, tg := range p.Targets {
			for _, s := range re.FindAllString(tg.Expr, -1) {
				if !strings.Contains(scraped, "\n"+s) {
					t.Errorf("dashboard panel %q: %s not served", p.Title, s)
				}
			}
		}
	}
}

// requireProbes: node plugin and controller containers have liveness, the Mount Pod readiness.
func requireProbes(ctx context.Context, t *testing.T, cs kubernetes.Interface, mp corev1.Pod) {
	t.Helper()
	for _, comp := range []string{"node", "controller"} {
		p, err := cs.CoreV1().Pods(driverNS()).Get(ctx, componentPod(ctx, t, cs, comp, ""), metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range p.Spec.Containers {
			if c.Name == "rclone" && c.LivenessProbe == nil {
				t.Errorf("%s rclone container has no liveness probe", comp)
			}
		}
	}
	if mp.Spec.Containers[0].ReadinessProbe == nil {
		t.Error("Mount Pod has no readiness probe")
	}
}
