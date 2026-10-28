//go:build e2e

package e2e

import (
	"context"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/utils/ptr"
)

// hack/e2e/up.sh enables mountPod.healWorkloads; bare pods in the other crash tests
// are not evicted, so they still see the manual-restart path.
func TestHealWorkloadAfterMountPodCrash(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	cs := client(t)
	setupNamespace(ctx, t, cs)
	createVolume(ctx, t, cs, staticPV("e2e-heal", map[string]string{
		"remote": "minio", "remotePath": "e2e-bucket/heal", "vfs-cache-mode": "writes",
	}))
	labels := map[string]string{"app": "e2e-heal"}
	tmpl := workload("", "e2e-heal", ioLoop)
	tmpl.Labels = labels
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "e2e-heal", Namespace: appNS},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To(int32(1)),
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{ObjectMeta: tmpl.ObjectMeta, Spec: tmpl.Spec},
		},
	}
	deps := cs.AppsV1().Deployments(appNS)
	_ = deps.Delete(ctx, dep.Name, metav1.DeleteOptions{})
	if _, err := deps.Create(ctx, dep, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = deps.Delete(context.Background(), dep.Name, metav1.DeleteOptions{PropagationPolicy: ptr.To(metav1.DeletePropagationForeground)})
	}()

	first := depPod(ctx, t, cs, "")
	waitLogContains(ctx, t, cs, appNS, first.Name, "IO-OK 3")
	old := soleMountPod(ctx, t, cs)

	killRclone(ctx, t, cs, old.Spec.NodeName)
	poll(ctx, t, "WorkloadHealed Event on the evicted pod", func() (bool, error) {
		return hasEvent(ctx, cs, appNS, first.Name, "WorkloadHealed")
	})
	next := depPod(ctx, t, cs, first.UID)
	waitLogContains(ctx, t, cs, appNS, next.Name, "IO-OK 3")
	if strings.Contains(podLog(ctx, t, cs, next.Name), "IO-FAIL") {
		t.Fatal("replacement pod fails")
	}
	t.Log("Deployment pod replaced and IO works again without manual action")

	if err := deps.Delete(ctx, dep.Name, metav1.DeleteOptions{PropagationPolicy: ptr.To(metav1.DeletePropagationForeground)}); err != nil {
		t.Fatal(err)
	}
	poll(ctx, t, "Deployment pods gone", func() (bool, error) {
		l, err := cs.CoreV1().Pods(appNS).List(ctx, metav1.ListOptions{LabelSelector: "app=e2e-heal"})
		return err == nil && len(l.Items) == 0, nil
	})
	poll(ctx, t, "Mount Pod gone", func() (bool, error) { return countMount(ctx, t, cs) == 0, nil })
}

// depPod waits for the running e2e-heal pod whose UID is not skip.
func depPod(ctx context.Context, t *testing.T, cs kubernetes.Interface, skip types.UID) corev1.Pod {
	t.Helper()
	var out corev1.Pod
	poll(ctx, t, "running e2e-heal pod", func() (bool, error) {
		l, err := cs.CoreV1().Pods(appNS).List(ctx, metav1.ListOptions{LabelSelector: "app=e2e-heal"})
		if err != nil {
			return false, err
		}
		for _, p := range l.Items {
			if p.UID != skip && p.DeletionTimestamp == nil && p.Status.Phase == corev1.PodRunning {
				out = p
				return true, nil
			}
		}
		return false, nil
	})
	return out
}
