//go:build e2e

// Package e2e runs against the cluster brought up by hack/e2e/up.sh (mise run e2e).
package e2e

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	driverName = "rclone.csi.blackdark.github.io"
	appNS      = "e2e-app"
	busybox    = "busybox:1.37.0@sha256:bdf57e528e45e4433820e045b29b4597825a1c9e38353532d90a01445013f82e"
	mcImage    = "cgr.dev/chainguard/minio-client@sha256:799a60aa839c0d79b53f1dccdddc974d0fe94282cca712fbfbad3b196f88d0ee"
	mcHost     = "http://e2e-access:e2e-secret-key@minio.minio.svc:9000"
	mountSel   = "app.kubernetes.io/name=csi-driver-rclone,app.kubernetes.io/component=mount"
	configData = `[minio]
type = s3
provider = Minio
access_key_id = e2e-access
secret_access_key = e2e-secret-key
endpoint = http://minio.minio.svc:9000
`
)

func driverNS() string {
	if ns := os.Getenv("DRIVER_NS"); ns != "" {
		return ns
	}
	return "csi-rclone"
}

func client(t *testing.T) kubernetes.Interface {
	t.Helper()
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{}).ClientConfig()
	if err != nil {
		t.Fatal(err)
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return cs
}

func TestStaticVolume(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	cs := client(t)
	setupNamespace(ctx, t, cs)
	marker := fmt.Sprintf("hello-%d", time.Now().UnixNano())

	pv := staticPV("e2e-static", map[string]string{
		"remote": "minio", "remotePath": "e2e-bucket", "vfs-cache-mode": "writes",
	})
	createVolume(ctx, t, cs, pv)
	pod := workload("e2e-writer", "e2e-static",
		fmt.Sprintf("echo %s > /data/marker.txt && cat /data/marker.txt && sleep 3600", marker))
	mustCreatePod(ctx, t, cs, pod)

	waitPodRunning(ctx, t, cs, appNS, pod.Name)
	waitLogContains(ctx, t, cs, appNS, pod.Name, marker)
	t.Log("pod wrote and read through the volume")

	if n := countMount(ctx, t, cs); n != 1 {
		t.Fatalf("want 1 Mount Pod and Secret pair, got %d", n)
	}
	poll(ctx, t, "object in MinIO", func() (bool, error) {
		out, err := runMC(ctx, cs, "cat", "e2e/e2e-bucket/marker.txt")
		return strings.Contains(out, marker), err
	})
	t.Log("object present in MinIO")

	deletePod(ctx, t, cs, pod.Name)
	poll(ctx, t, "Mount Pod and owned Secret gone", func() (bool, error) {
		return countMount(ctx, t, cs) == 0, nil
	})
	t.Log("Mount Pod and owned Secret gone")
}

func TestCredentialsInVolumeContextRejected(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	cs := client(t)
	setupNamespace(ctx, t, cs)
	pv := staticPV("e2e-bad", map[string]string{"configData": configData})
	createVolume(ctx, t, cs, pv)
	pod := workload("e2e-bad", "e2e-bad", "sleep 3600")
	mustCreatePod(ctx, t, cs, pod)
	defer deletePod(ctx, t, cs, pod.Name)

	poll(ctx, t, "FailedMount event with InvalidArgument", func() (bool, error) {
		evs, err := cs.CoreV1().Events(appNS).List(ctx, metav1.ListOptions{
			FieldSelector: "involvedObject.kind=Pod,involvedObject.name=" + pod.Name,
		})
		if err != nil {
			return false, err
		}
		for _, e := range evs.Items {
			if e.Reason == "FailedMount" && strings.Contains(e.Message, "InvalidArgument") {
				if strings.Contains(e.Message, "e2e-secret-key") {
					return false, fmt.Errorf("event leaks the credential: %s", e.Message)
				}
				t.Logf("event: %s", e.Message)
				return true, nil
			}
		}
		return false, nil
	})
}
