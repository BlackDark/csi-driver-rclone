//go:build e2e

package e2e

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
)

// hack/e2e/up.sh loads both tags; the rest of the suite runs on oldTag.
const (
	imageRepo = "csi-driver-rclone"
	oldTag    = "e2e"
	newTag    = "e2e-v2"
)

// helmSetTag upgrades the release to another image tag, keeping every other value.
func helmSetTag(t *testing.T, tag string) {
	t.Helper()
	out, err := exec.Command("helm", "upgrade", "csi-rclone", "../../charts/csi-driver-rclone",
		"-n", driverNS(), "--reuse-values", "--set", "image.tag="+tag,
		"--wait", "--timeout", "3m").CombinedOutput()
	if err != nil {
		t.Fatalf("helm upgrade to %s: %v\n%s", tag, err, out)
	}
}

func mountImage(p corev1.Pod) string { return p.Spec.Containers[0].Image }

// requireInfoImage: the node plugin reports the Mount Pod's actual image.
func requireInfoImage(ctx context.Context, t *testing.T, cs kubernetes.Interface, mp corev1.Pod, image string) {
	t.Helper()
	poll(ctx, t, "mount_pod_info image "+image, func() (bool, error) {
		node := scrape(ctx, t, cs, componentPod(ctx, t, cs, "node", mp.Spec.NodeName), "9809", "/metrics")
		for _, l := range strings.Split(node, "\n") {
			if strings.HasPrefix(l, "csi_rclone_mount_pod_info{") && strings.Contains(l, `image="`+image+`"`) {
				return true, nil
			}
		}
		return false, nil
	})
}

// Runs last (file order): it rolls the chart and restores it afterwards.
func TestUpgradeKeepsInUseMountPod(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	cs := client(t)
	setupNamespace(ctx, t, cs)
	vol := "e2e-upgrade"
	createVolume(ctx, t, cs, staticPV(vol, map[string]string{
		"remote": "minio", "remotePath": "e2e-bucket/upgrade", "vfs-cache-mode": "writes",
	}))
	oldImage, newImage := imageRepo+":"+oldTag, imageRepo+":"+newTag

	pod := workload("e2e-upgrade", vol, ioLoop)
	mustCreatePod(ctx, t, cs, pod)
	waitLogContains(ctx, t, cs, appNS, pod.Name, "IO-OK 3")
	mp := soleMountPod(ctx, t, cs)
	if got := mountImage(mp); got != oldImage {
		t.Fatalf("Mount Pod image %s, want %s", got, oldImage)
	}
	requireDefaultResources(t, mp)

	t.Cleanup(func() { helmSetTag(t, oldTag) })
	helmSetTag(t, newTag)
	plugin := nodePlugin(ctx, t, cs)
	if got := plugin.Spec.Containers[0].Image; got != newImage {
		t.Fatalf("node plugin image %s, want %s", got, newImage)
	}
	ioContinues(ctx, t, cs, pod.Name, lastOK(podLog(ctx, t, cs, pod.Name)))
	kept := soleMountPod(ctx, t, cs)
	if kept.UID != mp.UID || mountImage(kept) != oldImage {
		t.Fatalf("in-use Mount Pod replaced by the upgrade: %s %s, want %s %s", kept.UID, mountImage(kept), mp.UID, oldImage)
	}
	requireInfoImage(ctx, t, cs, kept, oldImage)
	t.Log("chart upgrade adopted the in-use Mount Pod with its old image")

	deletePod(ctx, t, cs, pod.Name)
	again := workload("e2e-upgrade-again", vol, ioLoop)
	mustCreatePod(ctx, t, cs, again)
	waitLogContains(ctx, t, cs, appNS, again.Name, "IO-OK 3")
	if got := soleMountPod(ctx, t, cs); got.UID != mp.UID {
		t.Fatalf("republish within the Linger replaced the Mount Pod: %s", got.UID)
	}
	t.Log("republish within the Linger reused the old-image Mount Pod")

	deletePod(ctx, t, cs, again.Name)
	poll(ctx, t, "old Mount Pod gone after the Linger", func() (bool, error) {
		return countMount(ctx, t, cs) == 0, nil
	})
	fresh := workload("e2e-upgrade-fresh", vol, ioLoop)
	mustCreatePod(ctx, t, cs, fresh)
	waitLogContains(ctx, t, cs, appNS, fresh.Name, "IO-OK 3")
	next := soleMountPod(ctx, t, cs)
	if mountImage(next) != newImage {
		t.Fatalf("new Mount Pod image %s, want %s", mountImage(next), newImage)
	}
	requireInfoImage(ctx, t, cs, next, newImage)
	t.Log("publish after the Linger started a Mount Pod on the new image")

	deletePod(ctx, t, cs, fresh.Name)
	poll(ctx, t, "Mount Pod gone", func() (bool, error) { return countMount(ctx, t, cs) == 0, nil })
}

// requireDefaultResources: chart defaults (values.yaml mountPod.resources), no memory limit.
func requireDefaultResources(t *testing.T, mp corev1.Pod) {
	t.Helper()
	r := mp.Spec.Containers[0].Resources
	if r.Requests.Memory().String() != "64Mi" || r.Requests.Cpu().String() != "10m" {
		t.Errorf("Mount Pod requests %v", r.Requests)
	}
	if _, ok := r.Limits[corev1.ResourceMemory]; ok {
		t.Errorf("Mount Pod memory limit set: %v", r.Limits)
	}
	if mp.Spec.PriorityClassName != "system-node-critical" || len(mp.Spec.Tolerations) == 0 {
		t.Errorf("priority %q tolerations %v", mp.Spec.PriorityClassName, mp.Spec.Tolerations)
	}
}
