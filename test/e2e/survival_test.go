//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/utils/ptr"
)

const (
	nodeSel = "app.kubernetes.io/name=csi-driver-rclone,app.kubernetes.io/component=node"
	// ioLoop writes and reads back a counter twice a second, logging each outcome.
	ioLoop = `i=0; while true; do i=$((i+1)); if echo $i > /data/io.txt && [ "$(cat /data/io.txt)" = "$i" ]; ` +
		`then echo "IO-OK $i"; else echo "IO-FAIL $i"; fi; sleep 0.5; done`
)

var ioOK = regexp.MustCompile(`IO-OK (\d+)`)

func podLog(ctx context.Context, t *testing.T, cs kubernetes.Interface, name string) string {
	t.Helper()
	b, err := cs.CoreV1().Pods(appNS).GetLogs(name, &corev1.PodLogOptions{}).DoRaw(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func lastOK(log string) int {
	m := ioOK.FindAllStringSubmatch(log, -1)
	if len(m) == 0 {
		return 0
	}
	n, _ := strconv.Atoi(m[len(m)-1][1])
	return n
}

// ioContinues fails on any IO-FAIL and waits for IO-OK past the current count.
func ioContinues(ctx context.Context, t *testing.T, cs kubernetes.Interface, name string, after int) {
	t.Helper()
	poll(ctx, t, "IO continuing", func() (bool, error) {
		log := podLog(ctx, t, cs, name)
		if strings.Contains(log, "IO-FAIL") {
			return false, fmt.Errorf("IO failed: %s", tail(log))
		}
		return lastOK(log) > after+4, nil
	})
}

func tail(s string) string {
	if len(s) > 400 {
		return s[len(s)-400:]
	}
	return s
}

func nodePlugin(ctx context.Context, t *testing.T, cs kubernetes.Interface) corev1.Pod {
	t.Helper()
	var pod corev1.Pod
	poll(ctx, t, "one ready node plugin", func() (bool, error) {
		l, err := cs.CoreV1().Pods(driverNS()).List(ctx, metav1.ListOptions{LabelSelector: nodeSel})
		if err != nil || len(l.Items) != 1 {
			return false, err
		}
		p := l.Items[0]
		if p.DeletionTimestamp != nil || p.Status.Phase != corev1.PodRunning {
			return false, nil
		}
		for _, c := range p.Status.ContainerStatuses {
			if !c.Ready {
				return false, nil
			}
		}
		pod = p
		return true, nil
	})
	return pod
}

func replaced(ctx context.Context, t *testing.T, cs kubernetes.Interface, old types.UID) {
	t.Helper()
	poll(ctx, t, "new node plugin", func() (bool, error) {
		return nodePlugin(ctx, t, cs).UID != old, nil
	})
}

func TestNodePluginRestartKeepsMounts(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	cs := client(t)
	setupNamespace(ctx, t, cs)
	createVolume(ctx, t, cs, staticPV("e2e-restart", map[string]string{
		"remote": "minio", "remotePath": "e2e-bucket/restart", "vfs-cache-mode": "writes",
	}))
	pod := workload("e2e-restart", "e2e-restart", ioLoop)
	mustCreatePod(ctx, t, cs, pod)
	waitLogContains(ctx, t, cs, appNS, pod.Name, "IO-OK 3")
	mp := soleMountPod(ctx, t, cs).UID

	plugin := nodePlugin(ctx, t, cs)
	if err := cs.CoreV1().Pods(driverNS()).Delete(ctx, plugin.Name, metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	replaced(ctx, t, cs, plugin.UID)
	ioContinues(ctx, t, cs, pod.Name, lastOK(podLog(ctx, t, cs, pod.Name)))
	t.Log("IO continued across a node plugin pod deletion")

	// What helm upgrade does to the DaemonSet: a new pod template, rolled out.
	plugin = nodePlugin(ctx, t, cs)
	patch := fmt.Sprintf(`{"spec":{"template":{"metadata":{"annotations":{"e2e/roll":"%d"}}}}}`, time.Now().UnixNano())
	if _, err := cs.AppsV1().DaemonSets(driverNS()).Patch(ctx, "csi-rclone-csi-driver-rclone-node",
		types.StrategicMergePatchType, []byte(patch), metav1.PatchOptions{}); err != nil {
		t.Fatal(err)
	}
	replaced(ctx, t, cs, plugin.UID)
	ioContinues(ctx, t, cs, pod.Name, lastOK(podLog(ctx, t, cs, pod.Name)))
	if got := soleMountPod(ctx, t, cs).UID; got != mp {
		t.Fatalf("Mount Pod replaced across restarts: %s, want %s", got, mp)
	}
	t.Log("IO continued across a DaemonSet rollout; same Mount Pod adopted")

	deletePod(ctx, t, cs, pod.Name)
	poll(ctx, t, "adopted Mount Pod gone after the Linger", func() (bool, error) {
		return countMount(ctx, t, cs) == 0, nil
	})
	t.Log("unpublish after restart released the adopted Mount Pod")
}

// killRclone SIGKILLs every rclone Mount Pod process on the node from a hostPID pod:
// the crash a Mount Pod suffers on OOM or a bug, without test hooks in the driver.
func killRclone(ctx context.Context, t *testing.T, cs kubernetes.Interface, nodeName string) {
	t.Helper()
	name := fmt.Sprintf("e2e-kill-%d", time.Now().UnixNano())
	p := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: driverNS()},
		Spec: corev1.PodSpec{
			NodeName: nodeName, HostPID: true, RestartPolicy: corev1.RestartPolicyNever,
			Containers: []corev1.Container{{
				Name: "kill", Image: busybox,
				Command:         []string{"sh", "-c", "pkill -9 -f '^/csi-driver-rclone mount' && echo KILLED"},
				SecurityContext: &corev1.SecurityContext{Privileged: ptr.To(true)},
			}},
		},
	}
	if _, err := cs.CoreV1().Pods(driverNS()).Create(ctx, p, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cs.CoreV1().Pods(driverNS()).Delete(context.Background(), name, metav1.DeleteOptions{}) }()
	poll(ctx, t, "rclone killed", func() (bool, error) {
		b, err := cs.CoreV1().Pods(driverNS()).GetLogs(name, &corev1.PodLogOptions{}).DoRaw(ctx)
		return err == nil && strings.Contains(string(b), "KILLED"), nil
	})
}

func hasEvent(ctx context.Context, cs kubernetes.Interface, ns, name, reason string) (bool, error) {
	evs, err := cs.CoreV1().Events(ns).List(ctx, metav1.ListOptions{FieldSelector: "involvedObject.name=" + name})
	if err != nil {
		return false, err
	}
	for _, e := range evs.Items {
		if e.Reason == reason {
			return true, nil
		}
	}
	return false, nil
}

func TestMountPodCrash(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	cs := client(t)
	setupNamespace(ctx, t, cs)
	createVolume(ctx, t, cs, staticPV("e2e-crash", map[string]string{
		"remote": "minio", "remotePath": "e2e-bucket/crash", "vfs-cache-mode": "writes",
	}))
	pod := workload("e2e-crash", "e2e-crash", ioLoop)
	mustCreatePod(ctx, t, cs, pod)
	waitLogContains(ctx, t, cs, appNS, pod.Name, "IO-OK 3")
	old := soleMountPod(ctx, t, cs)

	killRclone(ctx, t, cs, old.Spec.NodeName)
	waitLogContains(ctx, t, cs, appNS, pod.Name, "IO-FAIL")
	t.Log("workload IO fails after the rclone crash (ADR 0003: no resume)")
	poll(ctx, t, "MountLost Events on workload and Mount Pod", func() (bool, error) {
		w, err := hasEvent(ctx, cs, appNS, pod.Name, "MountLost")
		if err != nil || !w {
			return false, err
		}
		return hasEvent(ctx, cs, driverNS(), old.Name, "MountLost")
	})
	poll(ctx, t, "Mount Pod recreated", func() (bool, error) {
		p, err := cs.CoreV1().Pods(driverNS()).Get(ctx, old.Name, metav1.GetOptions{})
		return err == nil && p.UID != old.UID && p.Status.Phase == corev1.PodRunning, nil
	})
	poll(ctx, t, "pod volumeHealth Inaccessible", func() (bool, error) {
		p, err := cs.CoreV1().Pods(appNS).Get(ctx, pod.Name, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		for _, vh := range p.Status.VolumeHealth {
			for _, c := range vh.HealthConditions {
				if c.Status == corev1.VolumeHealthInaccessible {
					t.Logf("volumeHealth: %s %s", c.Reason, c.Message)
					return true, nil
				}
			}
		}
		return false, nil
	})

	// The documented remedy: restart the pod. It mounts through the new Mount Pod.
	deletePod(ctx, t, cs, pod.Name)
	again := workload("e2e-crash", "e2e-crash", ioLoop)
	mustCreatePod(ctx, t, cs, again)
	waitLogContains(ctx, t, cs, appNS, again.Name, "IO-OK 3")
	if strings.Contains(podLog(ctx, t, cs, again.Name), "IO-FAIL") {
		t.Fatal("restarted pod still fails")
	}
	t.Log("restarted pod works through the recreated Mount Pod")
	deletePod(ctx, t, cs, again.Name)
	poll(ctx, t, "Mount Pod gone", func() (bool, error) { return countMount(ctx, t, cs) == 0, nil })
}
