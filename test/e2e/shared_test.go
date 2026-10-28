//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
)

// The chart is installed with mountPod.linger=20s (hack/e2e/up.sh).

func TestSharedMountPodReadOnlyAndLinger(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	cs := client(t)
	setupNamespace(ctx, t, cs)
	vol := "e2e-shared"
	createVolume(ctx, t, cs, staticPV(vol, map[string]string{
		"remote": "minio", "remotePath": "e2e-bucket/shared", "vfs-cache-mode": "writes",
	}))

	rw := workload("e2e-shared-rw", "e2e-shared",
		"mkdir -p /all/ro && echo seed > /all/ro/seed.txt && echo rw > /data/f.txt && echo RW-OK && sleep 3600")
	rw.Spec.Containers[0].VolumeMounts = []corev1.VolumeMount{
		{Name: "data", MountPath: "/all"},
		{Name: "data", MountPath: "/data", SubPath: "rw"},
	}
	mustCreatePod(ctx, t, cs, rw)
	waitLogContains(ctx, t, cs, appNS, rw.Name, "RW-OK")

	ro := workload("e2e-shared-ro", "e2e-shared",
		"cat /data/seed.txt; if touch /data/x 2>/tmp/err; then echo WRITE-OK; else cat /tmp/err; fi; sleep 3600")
	ro.Spec.Containers[0].VolumeMounts[0].SubPath = "ro"
	ro.Spec.Volumes[0].PersistentVolumeClaim.ReadOnly = true
	mustCreatePod(ctx, t, cs, ro)
	waitLogContains(ctx, t, cs, appNS, ro.Name, "seed")
	waitLogContains(ctx, t, cs, appNS, ro.Name, "Read-only file system")
	t.Log("read-only pod reads its subPath and gets EROFS on write")

	uid := soleMountPod(ctx, t, cs, vol).UID
	t.Logf("both pods share Mount Pod %s", uid)

	deletePod(ctx, t, cs, rw.Name)
	deletePod(ctx, t, cs, ro.Name)
	again := workload("e2e-shared-again", "e2e-shared", "cat /data/rw/f.txt && echo AGAIN && sleep 3600")
	mustCreatePod(ctx, t, cs, again)
	waitLogContains(ctx, t, cs, appNS, again.Name, "AGAIN")
	if got := soleMountPod(ctx, t, cs, vol).UID; got != uid {
		t.Fatalf("republish within the Linger made a new Mount Pod: %s, want %s", got, uid)
	}
	t.Log("republish within the Linger reused the Mount Pod")

	deletePod(ctx, t, cs, again.Name)
	poll(ctx, t, "Mount Pod gone after the Linger", func() (bool, error) {
		return countMount(ctx, t, cs, vol) == 0, nil
	})
}

func TestWriteBackDrainedOnStop(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	cs := client(t)
	setupNamespace(ctx, t, cs)
	// Write-back outlasts the Linger, so the upload is still queued at SIGTERM.
	vol := "e2e-writeback"
	createVolume(ctx, t, cs, staticPV(vol, map[string]string{
		"remote": "minio", "remotePath": "e2e-bucket/wb", "vfs-cache-mode": "writes", "vfs-write-back": "60s",
	}))
	marker := fmt.Sprintf("wb-%d", time.Now().UnixNano())
	file := marker + ".txt" // unique per run: earlier runs leave their objects behind
	pod := workload("e2e-writeback", "e2e-writeback", fmt.Sprintf("echo %s > /data/%s && echo WROTE && sleep 3600", marker, file))
	mustCreatePod(ctx, t, cs, pod)
	waitLogContains(ctx, t, cs, appNS, pod.Name, "WROTE")
	deletePod(ctx, t, cs, pod.Name)

	poll(ctx, t, "Mount Pod terminating after the Linger", func() (bool, error) {
		return soleMountPod(ctx, t, cs, vol).DeletionTimestamp != nil, nil
	})
	out, _ := runMC(ctx, cs, "ls", "e2e/e2e-bucket/wb/")
	if strings.Contains(out, file) {
		t.Fatalf("object uploaded before the Mount Pod stopped; the test proves nothing: %s", out)
	}
	t.Log("upload still queued when the Mount Pod got SIGTERM")

	poll(ctx, t, "Mount Pod gone", func() (bool, error) {
		return countMount(ctx, t, cs, vol) == 0, nil
	})
	out, err := runMC(ctx, cs, "cat", "e2e/e2e-bucket/wb/"+file)
	if err != nil || !strings.Contains(out, marker) {
		t.Fatalf("drained object not in MinIO: %q %v", out, err)
	}
	t.Log("write-back upload drained to MinIO before the Mount Pod exited")
}

// soleMountPod fails unless volumeID has exactly one Mount Pod.
func soleMountPod(ctx context.Context, t *testing.T, cs kubernetes.Interface, volumeID string) corev1.Pod {
	t.Helper()
	pods, err := cs.CoreV1().Pods(driverNS()).List(ctx, metav1.ListOptions{LabelSelector: volumeSel(volumeID)})
	if err != nil {
		t.Fatal(err)
	}
	if len(pods.Items) != 1 {
		names := make([]types.UID, 0, len(pods.Items))
		for _, p := range pods.Items {
			names = append(names, p.UID)
		}
		t.Fatalf("want 1 Mount Pod, got %v", names)
	}
	return pods.Items[0]
}
