//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/utils/ptr"
)

const dynClass = "e2e-rclone-dynamic"

func TestDynamicVolumes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	cs := client(t)
	setupNamespace(ctx, t, cs)
	ensureStorageClass(ctx, t, cs)
	claims := []string{"e2e-dyn-a", "e2e-dyn-b"}
	for _, c := range claims {
		deleteClaim(ctx, t, cs, c)
	}
	pvs := map[string]string{}
	for _, c := range claims {
		pvs[c] = createDynamicClaim(ctx, t, cs, c)
	}
	t.Logf("provisioned %v", pvs)

	marker := fmt.Sprintf("dyn-%d", time.Now().UnixNano())
	pod := workload("e2e-dyn", claims[0], fmt.Sprintf(
		"echo %[1]s-a > /data/a.txt && echo %[1]s-b > /b/b.txt && ls /data /b && echo done && sleep 3600", marker))
	pod.Spec.Containers[0].VolumeMounts = append(pod.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{Name: "b", MountPath: "/b"})
	pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{Name: "b", VolumeSource: corev1.VolumeSource{
		PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: claims[1]},
	}})
	mustCreatePod(ctx, t, cs, pod)
	waitPodRunning(ctx, t, cs, appNS, pod.Name)
	waitLogContains(ctx, t, cs, appNS, pod.Name, "done")

	dir := func(c string) string { return "e2e/e2e-bucket/dyn/" + appNS + "/" + c + "/" }
	poll(ctx, t, "objects in per-PVC dirs", func() (bool, error) {
		a, err := runMC(ctx, cs, "cat", dir(claims[0])+"a.txt")
		if err != nil {
			return false, err
		}
		b, err := runMC(ctx, cs, "cat", dir(claims[1])+"b.txt")
		return strings.Contains(a, marker+"-a") && strings.Contains(b, marker+"-b"), err
	})
	for c, other := range map[string]string{claims[0]: "b.txt", claims[1]: "a.txt"} {
		out, err := runMC(ctx, cs, "ls", dir(c))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out, other) {
			t.Fatalf("%s not isolated: %s", c, out)
		}
	}
	t.Log("each PVC wrote to its own dir")

	deletePod(ctx, t, cs, pod.Name)
	poll(ctx, t, "Mount Pods gone", func() (bool, error) { return countMount(ctx, t, cs) == 0, nil })
	for _, c := range claims {
		deleteClaim(ctx, t, cs, c)
		poll(ctx, t, "PV "+pvs[c]+" gone", func() (bool, error) {
			_, err := cs.CoreV1().PersistentVolumes().Get(ctx, pvs[c], metav1.GetOptions{})
			return apierrors.IsNotFound(err), nil
		})
	}
	out, err := runMC(ctx, cs, "cat", dir(claims[0])+"a.txt")
	if err != nil || !strings.Contains(out, marker+"-a") {
		t.Fatalf("data gone after PVC delete: %q %v", out, err)
	}
	t.Log("PVs deleted, data remains in MinIO")
}

func ensureStorageClass(ctx context.Context, t *testing.T, cs kubernetes.Interface) {
	t.Helper()
	sc := &storagev1.StorageClass{
		ObjectMeta:    metav1.ObjectMeta{Name: dynClass},
		Provisioner:   driverName,
		ReclaimPolicy: ptr.To(corev1.PersistentVolumeReclaimDelete),
		Parameters: map[string]string{
			"remote":         "minio",
			"remotePath":     "e2e-bucket/dyn/${pvc.namespace}/${pvc.name}",
			"vfs-cache-mode": "writes",
			"csi.storage.k8s.io/node-publish-secret-name":      "minio-creds",
			"csi.storage.k8s.io/node-publish-secret-namespace": "${pvc.namespace}",
		},
	}
	_ = cs.StorageV1().StorageClasses().Delete(ctx, dynClass, metav1.DeleteOptions{})
	poll(ctx, t, "old StorageClass gone", func() (bool, error) {
		_, err := cs.StorageV1().StorageClasses().Get(ctx, dynClass, metav1.GetOptions{})
		return apierrors.IsNotFound(err), nil
	})
	if _, err := cs.StorageV1().StorageClasses().Create(ctx, sc, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
}

// createDynamicClaim returns the name of the PV provisioned for the claim.
func createDynamicClaim(ctx context.Context, t *testing.T, cs kubernetes.Interface, name string) string {
	t.Helper()
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: appNS},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
			StorageClassName: ptr.To(dynClass),
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
			},
		},
	}
	if _, err := cs.CoreV1().PersistentVolumeClaims(appNS).Create(ctx, pvc, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	var pv string
	poll(ctx, t, "PVC "+name+" bound", func() (bool, error) {
		c, err := cs.CoreV1().PersistentVolumeClaims(appNS).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		pv = c.Spec.VolumeName
		return c.Status.Phase == corev1.ClaimBound, nil
	})
	return pv
}

func deleteClaim(ctx context.Context, t *testing.T, cs kubernetes.Interface, name string) {
	t.Helper()
	_ = cs.CoreV1().PersistentVolumeClaims(appNS).Delete(ctx, name, metav1.DeleteOptions{})
	poll(ctx, t, "PVC "+name+" deleted", func() (bool, error) {
		_, err := cs.CoreV1().PersistentVolumeClaims(appNS).Get(ctx, name, metav1.GetOptions{})
		return apierrors.IsNotFound(err), nil
	})
}
