package mountpod

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	"github.com/BlackDark/csi-driver-rclone/internal/mountspec"
)

// Config is the node-wide part of every Mount Pod.
type Config struct {
	Namespace         string
	Image             string
	KubeletDir        string
	CacheRoot         string
	UID, GID          int64
	PriorityClassName string
	Tolerations       []corev1.Toleration
	// Resources are node-wide defaults; volume context keys override them per field.
	// Outside the Mount Spec, so changing them never replaces a Mount Pod.
	Resources    corev1.ResourceRequirements
	DrainTimeout time.Duration
	UnmountWait  time.Duration
}

// Input is one Mount Pod to build.
type Input struct {
	Config   Config
	NodeName string
	VolumeID string
	Spec     mountspec.Spec
}

// graceSlack covers process exit after the drain and the unmount wait.
const graceSlack = 10 * time.Second

// Build returns the Mount Pod and its Secret. The Secret's ownerReference needs the
// Pod's UID, so callers create the Pod first and then apply OwnedBy.
func Build(in Input) (corev1.Pod, corev1.Secret, error) {
	id := ID(in.Spec.Identity, in.NodeName)
	labels := Labels(in.NodeName, id)
	labels[LabelVolume] = CacheKey(in.VolumeID)
	meta := metav1.ObjectMeta{
		Name:        Name(id),
		Namespace:   in.Config.Namespace,
		Labels:      labels,
		Annotations: map[string]string{AnnotationVolID: in.VolumeID},
	}
	secret, err := buildSecret(meta, in.Spec)
	if err != nil {
		return corev1.Pod{}, corev1.Secret{}, err
	}
	resources, err := buildResources(in.Config.Resources, in.Spec.Resources)
	if err != nil {
		return corev1.Pod{}, corev1.Secret{}, err
	}
	cache, err := cacheSource(in)
	if err != nil {
		return corev1.Pod{}, corev1.Secret{}, err
	}
	pod := corev1.Pod{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"},
		ObjectMeta: *meta.DeepCopy(),
		Spec:       podSpec(in, id, resources, cache),
	}
	return pod, secret, nil
}

// OwnedBy returns s owned by the created pod, so it is garbage-collected with it.
func OwnedBy(s corev1.Secret, pod *corev1.Pod) corev1.Secret {
	out := *s.DeepCopy()
	out.OwnerReferences = []metav1.OwnerReference{{
		APIVersion: "v1",
		Kind:       "Pod",
		Name:       pod.Name,
		UID:        pod.UID,
	}}
	return out
}

func buildSecret(meta metav1.ObjectMeta, spec mountspec.Spec) (corev1.Secret, error) {
	file, err := spec.Serialize()
	if err != nil {
		return corev1.Secret{}, fmt.Errorf("mountpod: serialize spec: %w", err)
	}
	data := map[string][]byte{SpecKey: file}
	return corev1.Secret{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: *meta.DeepCopy(),
		Type:       corev1.SecretTypeOpaque,
		Data:       data,
	}, nil
}

func buildResources(defaults corev1.ResourceRequirements, r mountspec.Resources) (corev1.ResourceRequirements, error) {
	out := *defaults.DeepCopy()
	set := func(list *corev1.ResourceList, name corev1.ResourceName, key, v string) error {
		if v == "" {
			return nil
		}
		q, err := resource.ParseQuantity(v)
		if err != nil {
			return &mountspec.Error{Key: key, Reason: mountspec.ReasonInvalidValue, Detail: "want a Kubernetes quantity"}
		}
		if *list == nil {
			*list = corev1.ResourceList{}
		}
		(*list)[name] = q
		return nil
	}
	for _, f := range []func() error{
		func() error { return set(&out.Requests, corev1.ResourceCPU, mountspec.KeyCPURequest, r.CPURequest) },
		func() error { return set(&out.Limits, corev1.ResourceCPU, mountspec.KeyCPULimit, r.CPULimit) },
		func() error {
			return set(&out.Requests, corev1.ResourceMemory, mountspec.KeyMemoryRequest, r.MemoryRequest)
		},
		func() error { return set(&out.Limits, corev1.ResourceMemory, mountspec.KeyMemoryLimit, r.MemoryLimit) },
	} {
		if err := f(); err != nil {
			return out, err
		}
	}
	return out, nil
}

// cacheSource decodes cacheVolume strictly, or defaults to the node hostPath Cache.
func cacheSource(in Input) (corev1.VolumeSource, error) {
	if in.Spec.CacheVolume == "" {
		return corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{
			Path: CacheDir(in.Config.CacheRoot, in.VolumeID),
			Type: ptr.To(corev1.HostPathDirectoryOrCreate),
		}}, nil
	}
	var src corev1.VolumeSource
	dec := json.NewDecoder(bytes.NewReader([]byte(in.Spec.CacheVolume)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&src); err != nil {
		return src, &mountspec.Error{Key: mountspec.KeyCacheVolume, Reason: mountspec.ReasonInvalidValue, Detail: "want a Kubernetes volume source"}
	}
	return src, nil
}
