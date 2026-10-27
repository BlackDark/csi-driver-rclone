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
	"k8s.io/client-go/kubernetes"
	"k8s.io/utils/ptr"
)

func inlinePod(name string, attrs map[string]string, script string) *corev1.Pod {
	p := workload(name, "", script)
	p.Spec.Volumes = []corev1.Volume{{Name: "data", VolumeSource: corev1.VolumeSource{CSI: &corev1.CSIVolumeSource{
		Driver:               driverName,
		VolumeAttributes:     attrs,
		NodePublishSecretRef: &corev1.LocalObjectReference{Name: "minio-creds"},
		ReadOnly:             ptr.To(false),
	}}}}
	return p
}

// failedMount waits for kubelet's FailedMount Event on pod containing every part.
func failedMount(ctx context.Context, t *testing.T, cs kubernetes.Interface, pod string, parts ...string) {
	t.Helper()
	poll(ctx, t, "FailedMount on "+pod, func() (bool, error) {
		evs, err := cs.CoreV1().Events(appNS).List(ctx, metav1.ListOptions{
			FieldSelector: "involvedObject.kind=Pod,involvedObject.name=" + pod,
		})
		if err != nil {
			return false, err
		}
		for _, e := range evs.Items {
			if e.Reason != "FailedMount" {
				continue
			}
			ok := true
			for _, p := range parts {
				ok = ok && strings.Contains(e.Message, p)
			}
			if ok {
				t.Logf("event: %s", e.Message)
				return true, nil
			}
		}
		return false, nil
	})
}

func TestInlineVolume(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	cs := client(t)
	setupNamespace(ctx, t, cs)
	marker := fmt.Sprintf("inline-%d", time.Now().UnixNano())
	pod := inlinePod("e2e-inline", map[string]string{
		"remote": "minio", "remotePath": "e2e-bucket/inline", "vfs-cache-mode": "writes",
	}, fmt.Sprintf("echo %s > /data/marker.txt && cat /data/marker.txt && sleep 3600", marker))
	mustCreatePod(ctx, t, cs, pod)
	waitLogContains(ctx, t, cs, appNS, pod.Name, marker)
	t.Log("inline volume mounted with the pod-namespace Credential Secret")
	poll(ctx, t, "object in MinIO", func() (bool, error) {
		out, err := runMC(ctx, cs, "cat", "e2e/e2e-bucket/inline/marker.txt")
		return strings.Contains(out, marker), err
	})
	deletePod(ctx, t, cs, pod.Name)
	poll(ctx, t, "Mount Pod gone", func() (bool, error) { return countMount(ctx, t, cs) == 0, nil })
}

func TestInlineVolumeRejectsAdminOptions(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	cs := client(t)
	setupNamespace(ctx, t, cs)
	for name, extra := range map[string]map[string]string{
		"e2e-inline-unrestricted": {"allowAllOptions": "true"},
		"e2e-inline-tpslimit":     {"tpslimit": "8"},
	} {
		attrs := map[string]string{"remote": "minio", "remotePath": "e2e-bucket/inline"}
		var key string
		for k, v := range extra {
			attrs[k], key = v, k
		}
		pod := inlinePod(name, attrs, "sleep 3600")
		mustCreatePod(ctx, t, cs, pod)
		failedMount(ctx, t, cs, pod.Name, "InvalidArgument", fmt.Sprintf("%q", key), "not allowed for inline volumes")
		deletePod(ctx, t, cs, pod.Name)
	}
	if n := countMount(ctx, t, cs); n != 0 {
		t.Fatalf("rejected inline volumes started %d Mount Pods", n)
	}
}
