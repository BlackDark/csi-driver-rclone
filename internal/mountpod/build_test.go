package mountpod

import (
	"errors"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/BlackDark/csi-driver-rclone/internal/mountspec"
)

const (
	accessKey = "AKIASECRETVALUE"
	secretKey = "sup3r-s3cret-key"
	password  = "config-pass-value"
)

func testInput(t *testing.T, vc map[string]string) Input {
	t.Helper()
	spec, err := mountspec.Resolve(mountspec.Input{
		Secrets: map[string]string{
			"remoteType": "s3", "access_key_id": accessKey, "secret_access_key": secretKey,
			"configPassword": password,
		},
		VolumeContext: vc,
		VolumeID:      "vol-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	return Input{
		Config: Config{
			Namespace: "rclone-csi", Image: "ghcr.io/x/csi-driver-rclone:v1",
			KubeletDir: "/var/lib/kubelet", CacheRoot: "/var/lib/rclone-csi/cache",
			UID: 65532, GID: 65532, PriorityClassName: "system-node-critical",
			Tolerations:  []corev1.Toleration{{Operator: corev1.TolerationOpExists}},
			DrainTimeout: 5 * time.Minute, UnmountWait: 30 * time.Second,
		},
		NodeName: "node-a", VolumeID: "vol-1", Spec: spec,
	}
}

func build(t *testing.T, in Input) (corev1.Pod, corev1.Secret) {
	t.Helper()
	pod, secret, err := Build(in)
	if err != nil {
		t.Fatal(err)
	}
	return pod, secret
}

func TestNoCredentialsInPodSpec(t *testing.T) {
	pod, secret := build(t, testInput(t, nil))
	c := pod.Spec.Containers[0]
	var literal []string
	literal = append(literal, c.Args...)
	literal = append(literal, c.Command...)
	for _, e := range c.Env {
		literal = append(literal, e.Value)
	}
	for k, v := range pod.Annotations {
		literal = append(literal, k, v)
	}
	joined := strings.Join(literal, " ")
	for _, s := range []string{accessKey, secretKey, password} {
		if strings.Contains(joined, s) {
			t.Errorf("credential %q leaked into pod spec", s)
		}
		if strings.Contains(string(secret.Data[SpecKey]), password) {
			t.Error("config password in spec.json")
		}
	}
	for _, e := range c.Env {
		if e.Value != "" || e.ValueFrom == nil || e.ValueFrom.FieldRef == nil {
			t.Errorf("env %s is not a downward API field: %v", e.Name, e)
		}
	}
	if len(c.EnvFrom) != 0 {
		t.Errorf("unexpected envFrom: %v", c.EnvFrom)
	}
	if c.ReadinessProbe == nil || c.ReadinessProbe.HTTPGet.Path != "/healthz" || c.LivenessProbe != nil {
		t.Errorf("want readiness /healthz only: %v %v", c.ReadinessProbe, c.LivenessProbe)
	}
	if len(c.Ports) != 1 || c.Ports[0].ContainerPort != MetricsPort {
		t.Errorf("metrics port: %v", c.Ports)
	}
	if string(secret.Data[PasswordKey]) != password || !strings.Contains(string(secret.Data[SpecKey]), accessKey) {
		t.Error("secret lacks spec or password")
	}
}

func TestNamesLabelsAndPlacement(t *testing.T) {
	in := testInput(t, nil)
	pod, secret := build(t, in)
	id := ID(in.Spec.Identity, "node-a")
	if pod.Name != Name(id) || secret.Name != pod.Name || pod.Namespace != "rclone-csi" {
		t.Fatalf("names: %s %s", pod.Name, secret.Name)
	}
	if pod.Labels[LabelID] != id || pod.Labels[LabelNode] != "node-a" || secret.Labels[LabelComponent] != Component {
		t.Errorf("labels: %v %v", pod.Labels, secret.Labels)
	}
	if pod.Annotations[AnnotationVolID] != "vol-1" {
		t.Errorf("annotations: %v", pod.Annotations)
	}
	s := pod.Spec
	if s.NodeName != "node-a" || s.RestartPolicy != corev1.RestartPolicyNever ||
		s.PriorityClassName != "system-node-critical" || len(s.Tolerations) != 1 || *s.AutomountServiceAccountToken {
		t.Errorf("placement: %+v", s)
	}
	if *s.TerminationGracePeriodSeconds != 340 {
		t.Errorf("grace = %d", *s.TerminationGracePeriodSeconds)
	}
	if got := s.Containers[0].Args; got[0] != "mount" || got[1] != "--drain-timeout=5m0s" {
		t.Errorf("args: %v", got)
	}
}

func TestSecurityContext(t *testing.T) {
	pod, _ := build(t, testInput(t, nil))
	p := pod.Spec.SecurityContext
	if !*p.RunAsNonRoot || *p.RunAsUser != 65532 || *p.RunAsGroup != 65532 || p.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
		t.Errorf("pod securityContext: %+v", p)
	}
	c := pod.Spec.Containers[0].SecurityContext
	if *c.Privileged || *c.AllowPrivilegeEscalation || !*c.ReadOnlyRootFilesystem ||
		len(c.Capabilities.Drop) != 1 || c.Capabilities.Drop[0] != "ALL" || len(c.Capabilities.Add) != 0 {
		t.Errorf("container securityContext: %+v", c)
	}
}

func TestOwnedBy(t *testing.T) {
	pod, secret := build(t, testInput(t, nil))
	pod.UID = types.UID("uid-1")
	owned := OwnedBy(secret, &pod)
	if len(secret.OwnerReferences) != 0 {
		t.Error("OwnedBy mutated its input")
	}
	ref := owned.OwnerReferences
	if len(ref) != 1 || ref[0].Kind != "Pod" || ref[0].Name != pod.Name || ref[0].UID != "uid-1" {
		t.Errorf("ownerReferences: %+v", ref)
	}
}

func volume(pod corev1.Pod, name string) corev1.Volume {
	for _, v := range pod.Spec.Volumes {
		if v.Name == name {
			return v
		}
	}
	return corev1.Volume{}
}

func TestVolumes(t *testing.T) {
	in := testInput(t, nil)
	pod, _ := build(t, in)
	id := ID(in.Spec.Identity, "node-a")
	if s := volume(pod, volSecret).Secret; s == nil || s.SecretName != Name(id) || *s.Optional {
		t.Errorf("secret volume: %+v", s)
	}
	if h := volume(pod, volRun).HostPath; h == nil || h.Path != MountDir(in.Config.KubeletDir, id) {
		t.Errorf("run volume: %+v", h)
	}
	if pod.Labels[LabelVolume] != CacheKey("vol-1") {
		t.Errorf("volume label: %v", pod.Labels)
	}
	h := volume(pod, volCache).HostPath
	if h == nil || h.Path != "/var/lib/rclone-csi/cache/"+CacheKey("vol-1") || *h.Type != corev1.HostPathDirectoryOrCreate {
		t.Errorf("cache volume: %+v", h)
	}
}

func TestCacheVolumeSource(t *testing.T) {
	pod, _ := build(t, testInput(t, map[string]string{mountspec.KeyCacheVolume: `{"emptyDir":{"sizeLimit":"1Gi"}}`}))
	if e := volume(pod, volCache).EmptyDir; e == nil || e.SizeLimit.String() != "1Gi" {
		t.Errorf("cache volume: %+v", volume(pod, volCache))
	}
	_, _, err := Build(testInput(t, map[string]string{mountspec.KeyCacheVolume: `{"bogus":{}}`}))
	if !errors.Is(err, mountspec.ErrInvalid) {
		t.Errorf("unknown cache source: %v", err)
	}
}

func TestResources(t *testing.T) {
	pod, _ := build(t, testInput(t, map[string]string{mountspec.KeyCPULimit: "500m"}))
	r := pod.Spec.Containers[0].Resources
	if r.Limits.Cpu().String() != "500m" || r.Requests.Memory().String() != mountspec.DefaultMemoryRequest {
		t.Errorf("resources: %+v", r)
	}
	_, _, err := Build(testInput(t, map[string]string{mountspec.KeyMemoryLimit: "lots"}))
	if !errors.Is(err, mountspec.ErrInvalid) {
		t.Errorf("bad quantity: %v", err)
	}
}
