//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/BlackDark/csi-driver-rclone/internal/mountpod"
)

const cacheRoot = "/var/lib/rclone-csi/cache" // chart default

func kindNode() string {
	if n := os.Getenv("KIND_NODE"); n != "" {
		return n
	}
	return "csi-rclone-e2e-control-plane"
}

// cacheFiles lists the files named name in volumeID's hostPath Cache on the kind node.
func cacheFiles(t *testing.T, volumeID, name string) string {
	t.Helper()
	dir := mountpod.CacheDir(cacheRoot, volumeID)
	out, err := exec.Command("docker", "exec", kindNode(), "find", dir, "-type", "f", "-name", name).CombinedOutput()
	if err != nil {
		t.Fatalf("find in cache %s: %v: %s", dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestCacheSurvivesMountPodReplacement(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	cs := client(t)
	setupNamespace(ctx, t, cs)
	vol := "e2e-cache"
	createVolume(ctx, t, cs, staticPV(vol, map[string]string{
		"remote": "minio", "remotePath": "e2e-bucket/cache", "vfs-cache-mode": "full",
	}))
	marker := fmt.Sprintf("cached-%d", time.Now().UnixNano())
	pod := workload("e2e-cache", vol, fmt.Sprintf("echo %s > /data/cached.txt && cat /data/cached.txt && sleep 3600", marker))
	mustCreatePod(ctx, t, cs, pod)
	waitLogContains(ctx, t, cs, appNS, pod.Name, marker)
	first := soleMountPod(ctx, t, cs, vol)

	deletePod(ctx, t, cs, pod.Name)
	poll(ctx, t, "Mount Pod gone after the Linger", func() (bool, error) { return countMount(ctx, t, cs, vol) == 0, nil })
	if f := cacheFiles(t, vol, "cached.txt"); f == "" {
		t.Fatal("cached file not in the hostPath Cache after the Mount Pod stopped")
	}

	again := workload("e2e-cache", vol, "cat /data/cached.txt && sleep 3600")
	mustCreatePod(ctx, t, cs, again)
	waitLogContains(ctx, t, cs, appNS, again.Name, marker)
	if second := soleMountPod(ctx, t, cs, vol); second.UID == first.UID {
		t.Fatal("Mount Pod was not replaced")
	}
	f := cacheFiles(t, vol, "cached.txt")
	if f == "" {
		t.Fatal("cached file gone after the Mount Pod was recreated")
	}
	t.Logf("cache file kept across Mount Pod replacement: %s", f)
	deletePod(ctx, t, cs, again.Name)
	poll(ctx, t, "Mount Pod gone", func() (bool, error) { return countMount(ctx, t, cs, vol) == 0, nil })
}

func TestCacheVolumeSource(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	cs := client(t)
	setupNamespace(ctx, t, cs)
	vol := "e2e-cachevol"
	createVolume(ctx, t, cs, staticPV(vol, map[string]string{
		"remote": "minio", "remotePath": "e2e-bucket/cachevol", "vfs-cache-mode": "full",
		"cacheVolume": `{"emptyDir":{"sizeLimit":"256Mi"}}`,
	}))
	pod := workload("e2e-cachevol", vol, "echo ok > /data/x.txt && cat /data/x.txt && sleep 3600")
	mustCreatePod(ctx, t, cs, pod)
	waitLogContains(ctx, t, cs, appNS, pod.Name, "ok")
	mp := soleMountPod(ctx, t, cs, vol)
	found := false
	for _, v := range mp.Spec.Volumes {
		if v.Name == "cache" {
			found = v.EmptyDir != nil && v.EmptyDir.SizeLimit.String() == "256Mi"
		}
	}
	if !found {
		t.Fatalf("Mount Pod cache volume: %+v", mp.Spec.Volumes)
	}
	t.Log("cacheVolume emptyDir rendered and mounted")
	deletePod(ctx, t, cs, pod.Name)
	poll(ctx, t, "Mount Pod gone", func() (bool, error) { return countMount(ctx, t, cs, vol) == 0, nil })
}
