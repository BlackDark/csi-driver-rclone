//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/rclone/rclone/fs/config/obscure"
)

// chainConfig wraps MinIO like a real media setup: chunker → crypt → cache → s3.
// The cache backend writes its db under the user cache dir at NewFs time.
func chainConfig(t *testing.T) string {
	t.Helper()
	pw, err := obscure.Obscure("e2e-crypt-password")
	if err != nil {
		t.Fatal(err)
	}
	return configData + `
[chaincache]
type = cache
remote = minio:e2e-bucket/chain

[chaincrypt]
type = crypt
remote = chaincache:
password = ` + pw + `

[chain]
type = chunker
remote = chaincrypt:
`
}

func TestWrappedRemoteChain(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	cs := client(t)
	setupNamespace(ctx, t, cs)
	sec := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "chain-creds", Namespace: appNS},
		StringData: map[string]string{"configData": chainConfig(t)},
	}
	if _, err := cs.CoreV1().Secrets(appNS).Create(ctx, sec, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatal(err)
	}
	marker := fmt.Sprintf("chain-%d", time.Now().UnixNano())
	pv := staticPV("e2e-chain", map[string]string{"remote": "chain", "vfs-cache-mode": "writes"})
	pv.Spec.CSI.NodePublishSecretRef.Name = sec.Name
	createVolume(ctx, t, cs, pv)
	pod := workload("e2e-chain-writer", pv.Name,
		fmt.Sprintf("echo %s > /data/marker.txt && cat /data/marker.txt && sleep 3600", marker))
	mustCreatePod(ctx, t, cs, pod)
	waitPodRunning(ctx, t, cs, appNS, pod.Name)
	waitLogContains(ctx, t, cs, appNS, pod.Name, marker)

	poll(ctx, t, "encrypted object in MinIO", func() (bool, error) {
		out, err := runMC(ctx, cs, "ls", "--recursive", "e2e/e2e-bucket/chain/")
		return err == nil && strings.TrimSpace(out) != "" && !strings.Contains(out, "marker.txt"), nil
	})
	deletePod(ctx, t, cs, pod.Name)
	poll(ctx, t, "Mount Pod gone", func() (bool, error) {
		return countMount(ctx, t, cs, pv.Name) == 0, nil
	})
}
