//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/utils/ptr"
)

func setupNamespace(ctx context.Context, t *testing.T, cs kubernetes.Interface) {
	t.Helper()
	_, err := cs.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: appNS}}, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatal(err)
	}
	sec := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "minio-creds", Namespace: appNS},
		StringData: map[string]string{"configData": configData},
	}
	_, err = cs.CoreV1().Secrets(appNS).Create(ctx, sec, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatal(err)
	}
}

func staticPV(name string, attrs map[string]string) *corev1.PersistentVolume {
	return &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: corev1.PersistentVolumeSpec{
			Capacity:                      corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
			AccessModes:                   []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
			PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimRetain,
			StorageClassName:              "",
			ClaimRef:                      &corev1.ObjectReference{Namespace: appNS, Name: name},
			PersistentVolumeSource: corev1.PersistentVolumeSource{CSI: &corev1.CSIPersistentVolumeSource{
				Driver:               driverName,
				VolumeHandle:         name,
				VolumeAttributes:     attrs,
				NodePublishSecretRef: &corev1.SecretReference{Name: "minio-creds", Namespace: appNS},
			}},
		},
	}
}

// createVolume replaces any PV/PVC left by an earlier run.
func createVolume(ctx context.Context, t *testing.T, cs kubernetes.Interface, pv *corev1.PersistentVolume) {
	t.Helper()
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: pv.Name, Namespace: appNS},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
			StorageClassName: ptr.To(""),
			VolumeName:       pv.Name,
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
			},
		},
	}
	_ = cs.CoreV1().PersistentVolumeClaims(appNS).Delete(ctx, pvc.Name, metav1.DeleteOptions{})
	_ = cs.CoreV1().PersistentVolumes().Delete(ctx, pv.Name, metav1.DeleteOptions{})
	poll(ctx, t, "old PV gone", func() (bool, error) {
		_, err := cs.CoreV1().PersistentVolumes().Get(ctx, pv.Name, metav1.GetOptions{})
		return apierrors.IsNotFound(err), nil
	})
	if _, err := cs.CoreV1().PersistentVolumes().Create(ctx, pv, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := cs.CoreV1().PersistentVolumeClaims(appNS).Create(ctx, pvc, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
}

func workload(name, claim, script string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: appNS},
		Spec: corev1.PodSpec{
			TerminationGracePeriodSeconds: ptr.To(int64(1)),
			Containers: []corev1.Container{{
				Name: "app", Image: busybox, Command: []string{"sh", "-c", script},
				VolumeMounts: []corev1.VolumeMount{{Name: "data", MountPath: "/data"}},
			}},
			Volumes: []corev1.Volume{{Name: "data", VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: claim},
			}}},
		},
	}
}

func mustCreatePod(ctx context.Context, t *testing.T, cs kubernetes.Interface, pod *corev1.Pod) {
	t.Helper()
	deletePod(ctx, t, cs, pod.Name)
	if _, err := cs.CoreV1().Pods(appNS).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
}

func deletePod(ctx context.Context, t *testing.T, cs kubernetes.Interface, name string) {
	t.Helper()
	_ = cs.CoreV1().Pods(appNS).Delete(ctx, name, metav1.DeleteOptions{})
	poll(ctx, t, "pod "+name+" deleted", func() (bool, error) {
		_, err := cs.CoreV1().Pods(appNS).Get(ctx, name, metav1.GetOptions{})
		return apierrors.IsNotFound(err), nil
	})
}

func waitPodRunning(ctx context.Context, t *testing.T, cs kubernetes.Interface, ns, name string) {
	t.Helper()
	poll(ctx, t, "pod "+name+" running", func() (bool, error) {
		p, err := cs.CoreV1().Pods(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		return p.Status.Phase == corev1.PodRunning, nil
	})
}

func waitLogContains(ctx context.Context, t *testing.T, cs kubernetes.Interface, ns, name, want string) {
	t.Helper()
	poll(ctx, t, "log of "+name, func() (bool, error) {
		b, err := cs.CoreV1().Pods(ns).GetLogs(name, &corev1.PodLogOptions{}).DoRaw(ctx)
		return err == nil && strings.Contains(string(b), want), nil
	})
}

// countMount is the number of Mount Pods plus owned Secrets in the driver namespace.
func countMount(ctx context.Context, t *testing.T, cs kubernetes.Interface) int {
	t.Helper()
	pods, err := cs.CoreV1().Pods(driverNS()).List(ctx, metav1.ListOptions{LabelSelector: mountSel})
	if err != nil {
		t.Fatal(err)
	}
	secs, err := cs.CoreV1().Secrets(driverNS()).List(ctx, metav1.ListOptions{LabelSelector: mountSel})
	if err != nil {
		t.Fatal(err)
	}
	if len(pods.Items) != len(secs.Items) {
		return len(pods.Items) + len(secs.Items) + 1 // never equals a clean pair
	}
	return len(pods.Items)
}

// runMC runs one mc command as a Job and returns its log.
func runMC(ctx context.Context, cs kubernetes.Interface, args ...string) (string, error) {
	name := fmt.Sprintf("mc-%d", time.Now().UnixNano())
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "minio"},
		Spec: batchv1.JobSpec{
			BackoffLimit:            ptr.To(int32(0)),
			TTLSecondsAfterFinished: ptr.To(int32(60)),
			Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
				RestartPolicy: corev1.RestartPolicyNever,
				Containers: []corev1.Container{{
					Name: "mc", Image: mcImage, Args: args,
					Env: []corev1.EnvVar{{Name: "HOME", Value: "/tmp"}, {Name: "MC_HOST_e2e", Value: mcHost}},
				}},
			}},
		},
	}
	if _, err := cs.BatchV1().Jobs("minio").Create(ctx, job, metav1.CreateOptions{}); err != nil {
		return "", err
	}
	sel := "job-name=" + name
	var out string
	err := wait.PollUntilContextTimeout(ctx, time.Second, 90*time.Second, true, func(ctx context.Context) (bool, error) {
		pods, err := cs.CoreV1().Pods("minio").List(ctx, metav1.ListOptions{LabelSelector: sel})
		if err != nil || len(pods.Items) == 0 {
			return false, err
		}
		p := pods.Items[0]
		if p.Status.Phase != corev1.PodSucceeded && p.Status.Phase != corev1.PodFailed {
			return false, nil
		}
		b, err := cs.CoreV1().Pods("minio").GetLogs(p.Name, &corev1.PodLogOptions{}).DoRaw(ctx)
		out = string(b)
		return true, err
	})
	_ = cs.BatchV1().Jobs("minio").Delete(ctx, name, metav1.DeleteOptions{PropagationPolicy: ptr.To(metav1.DeletePropagationBackground)})
	return out, err
}

func poll(ctx context.Context, t *testing.T, what string, f func() (bool, error)) {
	t.Helper()
	err := wait.PollUntilContextTimeout(ctx, 2*time.Second, 3*time.Minute, true, func(context.Context) (bool, error) {
		return f()
	})
	if err != nil {
		t.Fatalf("waiting for %s: %v", what, err)
	}
}
